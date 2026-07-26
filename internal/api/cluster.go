package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/service"
)

// ClusterAPI exposes the node registry to administrators.
type ClusterAPI struct{}

type clusterNodeResponse struct {
	ID         uint      `json:"id"`
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	Online     bool      `json:"online"`
	IsCurrent  bool      `json:"is_current"`
	LastSeenAt time.Time `json:"last_seen_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type clusterStatusResponse struct {
	MultiNodeEnabled bool                  `json:"multi_node_enabled"`
	CurrentNodeName  string                `json:"current_node_name"`
	NodeNameMissing  bool                  `json:"node_name_missing"`
	AddressMode      string                `json:"node_address_mode"`
	Addresses        string                `json:"node_addresses"`
	Nodes            []clusterNodeResponse `json:"nodes"`
}

func (api *ClusterAPI) Status(c *gin.Context) {
	nodes, err := service.ListClusterNodes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load nodes"})
		return
	}
	currentName := service.CurrentNodeName()
	response := clusterStatusResponse{
		MultiNodeEnabled: service.MultiNodeEnabled(),
		CurrentNodeName:  currentName,
		NodeNameMissing:  service.MultiNodeEnabled() && currentName == "",
		AddressMode:      settingString("node_address_mode", "single"),
		Addresses:        settingString("node_addresses", ""),
		Nodes:            make([]clusterNodeResponse, 0, len(nodes)),
	}
	for _, node := range nodes {
		response.Nodes = append(response.Nodes, clusterNodeResponse{
			ID:         node.ID,
			Name:       node.Name,
			Role:       node.Role,
			Online:     service.NodeOnline(node),
			IsCurrent:  node.Name == currentName,
			LastSeenAt: node.LastSeenAt,
			CreatedAt:  node.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, response)
}

func (api *ClusterAPI) PromoteNode(c *gin.Context) {
	nodeID, err := parseClusterNodeID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	node, err := service.PromoteClusterNode(nodeID)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	service.RecordAuditLog(service.AuditLogInput{
		LogType:  service.AuditLogTypeSystem,
		Action:   "node_promoted",
		Resource: "cluster_node",
		Message:  "promoted node " + node.Name + " to primary",
	})
	c.JSON(http.StatusOK, gin.H{"message": "Node promoted", "name": node.Name})
}

func (api *ClusterAPI) DeleteNode(c *gin.Context) {
	nodeID, err := parseClusterNodeID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := service.DeleteClusterNode(nodeID); err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Node removed"})
}

func parseClusterNodeID(c *gin.Context) (uint, error) {
	raw := strings.TrimSpace(c.Param("id"))
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, errors.New("invalid node id")
	}
	return uint(value), nil
}

// RejectMultiNodeWithoutName guards the settings update so enabling multi-node
// mode cannot lock this instance out of booting: the node needs NODE_NAME to
// register itself, and startup fails without it.
func RejectMultiNodeWithoutName(enabling bool) error {
	if !enabling {
		return nil
	}
	if service.CurrentNodeName() == "" {
		return errors.New("NODE_NAME must be set in this node's environment before enabling multi-node mode")
	}
	return nil
}
