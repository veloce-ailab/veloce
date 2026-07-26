package model

import "time"

const (
	// NodeRolePrimary marks the node that owns scheduled background work.
	NodeRolePrimary = "primary"
	// NodeRoleReplica marks a node that only serves requests.
	NodeRoleReplica = "replica"
)

// ClusterNode records one deployment instance sharing this database.
//
// The first node to register claims the primary role; every node registering
// afterwards joins as a replica. Nodes identify themselves by the NODE_NAME
// environment variable, so a restarted or redeployed instance keeps its role.
type ClusterNode struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Name       string    `gorm:"uniqueIndex;size:100;not null" json:"name"`
	Role       string    `gorm:"size:16;not null;default:replica" json:"role"`
	LastSeenAt time.Time `gorm:"index" json:"last_seen_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (node ClusterNode) IsPrimary() bool {
	return node.Role == NodeRolePrimary
}
