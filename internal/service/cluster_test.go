package service

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func newClusterDB(t *testing.T, multiNode bool) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:cluster-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.ClusterNode{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&model.SystemSetting{Key: "multi_node_enabled", Value: boolSettingValue(multiNode)}).Error; err != nil {
		t.Fatal(err)
	}
	previousDB := model.DB
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
	invalidateNodeRoleCache()
	t.Cleanup(invalidateNodeRoleCache)
	return database
}

func boolSettingValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func useNodeName(t *testing.T, name string) {
	t.Helper()
	previous := config.NodeName
	config.NodeName = name
	t.Cleanup(func() { config.NodeName = previous })
	invalidateNodeRoleCache()
}

// The first node to register owns the scheduled work, so an existing
// single-node deployment keeps behaving the same after enabling the feature.
func TestEnsureCurrentNodeRegisteredClaimsPrimaryFirst(t *testing.T) {
	database := newClusterDB(t, true)
	useNodeName(t, "node-a")

	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatalf("register error = %v", err)
	}
	var node model.ClusterNode
	if err := database.Where("name = ?", "node-a").First(&node).Error; err != nil {
		t.Fatal(err)
	}
	if node.Role != model.NodeRolePrimary {
		t.Fatalf("role = %q, want %q", node.Role, model.NodeRolePrimary)
	}
	if !IsPrimaryNode() {
		t.Fatal("the first node must report itself as primary")
	}
}

func TestEnsureCurrentNodeRegisteredJoinsAsReplica(t *testing.T) {
	database := newClusterDB(t, true)
	useNodeName(t, "node-a")
	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatal(err)
	}

	useNodeName(t, "node-b")
	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatalf("register error = %v", err)
	}
	var replica model.ClusterNode
	if err := database.Where("name = ?", "node-b").First(&replica).Error; err != nil {
		t.Fatal(err)
	}
	if replica.Role != model.NodeRoleReplica {
		t.Fatalf("role = %q, want %q", replica.Role, model.NodeRoleReplica)
	}
	if IsPrimaryNode() {
		t.Fatal("a replica must not claim the primary role")
	}
}

// Re-registering keeps the role so a restart or redeploy does not demote the
// primary or hand ownership to whoever booted last.
func TestEnsureCurrentNodeRegisteredKeepsRoleOnRestart(t *testing.T) {
	database := newClusterDB(t, true)
	useNodeName(t, "node-a")
	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatal(err)
	}
	useNodeName(t, "node-b")
	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatal(err)
	}

	useNodeName(t, "node-a")
	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatal(err)
	}
	var node model.ClusterNode
	if err := database.Where("name = ?", "node-a").First(&node).Error; err != nil {
		t.Fatal(err)
	}
	if node.Role != model.NodeRolePrimary {
		t.Fatalf("role after restart = %q, want it unchanged", node.Role)
	}
	var count int64
	if err := database.Model(&model.ClusterNode{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("node rows = %d, want 2 (no duplicate registration)", count)
	}
}

func TestEnsureCurrentNodeRegisteredRequiresNodeName(t *testing.T) {
	newClusterDB(t, true)
	useNodeName(t, "")

	if err := EnsureCurrentNodeRegistered(); !errors.Is(err, ErrMultiNodeNameMissing) {
		t.Fatalf("register error = %v, want ErrMultiNodeNameMissing", err)
	}
	if IsPrimaryNode() {
		t.Fatal("a cluster member without a name must not claim shared work")
	}
}

// With the feature off there is only one process, so it must own the scheduled
// work and register nothing.
func TestSingleNodeModeIsAlwaysPrimary(t *testing.T) {
	database := newClusterDB(t, false)
	useNodeName(t, "")

	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatalf("register error = %v", err)
	}
	if !IsPrimaryNode() {
		t.Fatal("single-node mode must report primary")
	}
	var count int64
	if err := database.Model(&model.ClusterNode{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("node rows = %d, want none while multi-node mode is off", count)
	}
}

func TestPromoteClusterNodeMovesPrimary(t *testing.T) {
	database := newClusterDB(t, true)
	useNodeName(t, "node-a")
	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatal(err)
	}
	useNodeName(t, "node-b")
	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatal(err)
	}
	var replica model.ClusterNode
	if err := database.Where("name = ?", "node-b").First(&replica).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := PromoteClusterNode(replica.ID); err != nil {
		t.Fatalf("promote error = %v", err)
	}
	var nodes []model.ClusterNode
	if err := database.Order("name ASC").Find(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	if nodes[0].Role != model.NodeRoleReplica {
		t.Fatalf("previous primary role = %q, want demoted", nodes[0].Role)
	}
	if nodes[1].Role != model.NodeRolePrimary {
		t.Fatalf("promoted role = %q, want primary", nodes[1].Role)
	}
	// node-b is the running node in this test, so it now owns scheduled work.
	if !IsPrimaryNode() {
		t.Fatal("promotion must take effect without a restart")
	}
}

func TestDeleteClusterNodeRefusesCurrentAndOnlineNodes(t *testing.T) {
	database := newClusterDB(t, true)
	useNodeName(t, "node-a")
	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatal(err)
	}
	useNodeName(t, "node-b")
	if err := EnsureCurrentNodeRegistered(); err != nil {
		t.Fatal(err)
	}

	var current model.ClusterNode
	if err := database.Where("name = ?", "node-b").First(&current).Error; err != nil {
		t.Fatal(err)
	}
	if err := DeleteClusterNode(current.ID); err == nil {
		t.Fatal("the running node must not delete its own record")
	}

	var other model.ClusterNode
	if err := database.Where("name = ?", "node-a").First(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := DeleteClusterNode(other.ID); err == nil {
		t.Fatal("an online node must not be removed")
	}

	// Stale heartbeat: the node is gone and may be cleaned up.
	stale := time.Now().Add(-10 * time.Minute)
	if err := database.Model(&model.ClusterNode{}).Where("id = ?", other.ID).
		Update("last_seen_at", stale).Error; err != nil {
		t.Fatal(err)
	}
	if err := DeleteClusterNode(other.ID); err != nil {
		t.Fatalf("deleting an offline node error = %v", err)
	}
}
