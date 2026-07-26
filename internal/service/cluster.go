package service

import (
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

// nodeOfflineAfter is how long a node may go without a heartbeat before the
// admin UI reports it offline.
const nodeOfflineAfter = 90 * time.Second

const nodeHeartbeatInterval = 30 * time.Second

// nodeRoleCacheTTL keeps the background loops from querying the node table on
// every tick while still noticing a promotion within a few seconds.
const nodeRoleCacheTTL = 5 * time.Second

var (
	ErrMultiNodeNameMissing = errors.New("NODE_NAME must be set when multi-node mode is enabled")
	ErrNodeNotFound         = errors.New("node not found")
)

var nodeRoleCache struct {
	mu        sync.Mutex
	primary   bool
	expiresAt time.Time
}

// MultiNodeEnabled reports whether this deployment runs as a cluster. With it
// off the process is the only node and therefore always the primary.
func MultiNodeEnabled() bool {
	return settingBool("multi_node_enabled", false)
}

// CurrentNodeName is the identity this process registers under.
func CurrentNodeName() string {
	return strings.TrimSpace(config.NodeName)
}

// EnsureCurrentNodeRegistered records this process in the node table.
//
// The first node ever registered becomes the primary, which makes an existing
// single-node deployment keep its behavior after multi-node mode is turned on.
// Every later node joins as a replica so that scheduled work is not duplicated.
func EnsureCurrentNodeRegistered() error {
	if !MultiNodeEnabled() {
		return nil
	}
	name := CurrentNodeName()
	if name == "" {
		return ErrMultiNodeNameMissing
	}
	invalidateNodeRoleCache()
	return model.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		var existing model.ClusterNode
		err := tx.Where("name = ?", name).First(&existing).Error
		if err == nil {
			return tx.Model(&model.ClusterNode{}).Where("id = ?", existing.ID).
				Update("last_seen_at", now).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var registered int64
		if err := tx.Model(&model.ClusterNode{}).Count(&registered).Error; err != nil {
			return err
		}
		role := model.NodeRoleReplica
		if registered == 0 {
			role = model.NodeRolePrimary
		}
		node := model.ClusterNode{Name: name, Role: role, LastSeenAt: now}
		if err := tx.Create(&node).Error; err != nil {
			return err
		}
		log.Printf("registered node %q as %s", name, role)
		return nil
	})
}

// StartNodeHeartbeat keeps this node's last-seen timestamp fresh so the admin
// UI can tell running nodes from stale records.
func StartNodeHeartbeat() {
	go func() {
		ticker := time.NewTicker(nodeHeartbeatInterval)
		defer ticker.Stop()
		for range ticker.C {
			if !MultiNodeEnabled() {
				continue
			}
			name := CurrentNodeName()
			if name == "" {
				continue
			}
			// A node enabled at runtime may not have a row yet.
			if err := EnsureCurrentNodeRegistered(); err != nil {
				log.Printf("node heartbeat failed: %v", err)
			}
		}
	}()
}

// IsPrimaryNode reports whether this process owns the scheduled background work
// (price sync, status monitoring, reliability probing, log cleanup, task
// dispatch). Replicas only serve requests, so those jobs neither run twice nor
// probe upstreams once per node.
func IsPrimaryNode() bool {
	if !MultiNodeEnabled() {
		return true
	}
	name := CurrentNodeName()
	if name == "" {
		// Misconfigured cluster member: never claim ownership of shared work.
		return false
	}

	nodeRoleCache.mu.Lock()
	if time.Now().Before(nodeRoleCache.expiresAt) {
		primary := nodeRoleCache.primary
		nodeRoleCache.mu.Unlock()
		return primary
	}
	nodeRoleCache.mu.Unlock()

	var node model.ClusterNode
	if err := model.DB.Where("name = ?", name).First(&node).Error; err != nil {
		return false
	}
	primary := node.IsPrimary()

	nodeRoleCache.mu.Lock()
	nodeRoleCache.primary = primary
	nodeRoleCache.expiresAt = time.Now().Add(nodeRoleCacheTTL)
	nodeRoleCache.mu.Unlock()
	return primary
}

func invalidateNodeRoleCache() {
	nodeRoleCache.mu.Lock()
	nodeRoleCache.expiresAt = time.Time{}
	nodeRoleCache.mu.Unlock()
}

// ListClusterNodes returns every registered node, primary first.
func ListClusterNodes() ([]model.ClusterNode, error) {
	var nodes []model.ClusterNode
	err := model.DB.Order("role ASC, name ASC").Find(&nodes).Error
	return nodes, err
}

// NodeOnline reports whether the node reported a heartbeat recently enough.
func NodeOnline(node model.ClusterNode) bool {
	return time.Since(node.LastSeenAt) <= nodeOfflineAfter
}

// PromoteClusterNode makes the given node the primary and demotes the previous
// one, which is how a cluster recovers when the primary is gone for good.
func PromoteClusterNode(nodeID uint) (model.ClusterNode, error) {
	var promoted model.ClusterNode
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&promoted, nodeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNodeNotFound
			}
			return err
		}
		if err := tx.Model(&model.ClusterNode{}).
			Where("role = ? AND id <> ?", model.NodeRolePrimary, promoted.ID).
			Update("role", model.NodeRoleReplica).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ClusterNode{}).Where("id = ?", promoted.ID).
			Update("role", model.NodeRolePrimary).Error; err != nil {
			return err
		}
		promoted.Role = model.NodeRolePrimary
		return nil
	})
	invalidateNodeRoleCache()
	return promoted, err
}

// DeleteClusterNode removes a stale node record. The running node cannot delete
// itself, and a node still sending heartbeats is kept so the list reflects
// reality rather than a stale click.
func DeleteClusterNode(nodeID uint) error {
	var node model.ClusterNode
	if err := model.DB.First(&node, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNodeNotFound
		}
		return err
	}
	if node.Name == CurrentNodeName() {
		return errors.New("cannot remove the node serving this request")
	}
	if NodeOnline(node) {
		return errors.New("node is still online; stop it before removing the record")
	}
	if err := model.DB.Delete(&model.ClusterNode{}, node.ID).Error; err != nil {
		return err
	}
	invalidateNodeRoleCache()
	return nil
}
