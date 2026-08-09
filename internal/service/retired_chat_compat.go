package service

import (
	"context"
	"errors"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

// ErrRetiredOnlineChat is returned by compatibility paths that used to back
// the browser chat feature. The feature data is removed during startup.
var ErrRetiredOnlineChat = errors.New("online chat has been removed")

// These inert records keep unrelated enterprise cleanup paths buildable while
// the retired browser-chat implementation and its database tables are gone.
type AdvancedChatSession struct {
	ID                       string
	UserID                   uint
	FolderID                 string
	Title                    string
	RunMode                  string
	AgentID                  string
	SkillIDs                 string
	MCPServerIDs             string
	ConnectorApprovalMode    string
	ConnectorCommandPrefixes string
	ModelName                string
	UserChannelID            uint
}

type AdvancedChatMessage struct {
	ID           string
	SessionID    string
	UserID       uint
	Role         string
	Content      string
	ContentParts string
	ToolCalls    string
	SortOrder    int
}

type AdvancedChatFile struct {
	ID          string
	UserID      uint
	Name        string
	MIMEType    string
	TextExtract string
}

type AdvancedChatConnectorDevice struct {
	ID         string
	Name       string
	Mode       string
	ListenPort int
	Status     string
	Hostname   string
	OS         string
	LastSeenAt *time.Time
}

type AdvancedChatConnectorTask struct {
	ID       string
	DeviceID string
}

type MessageChannelConnectorApproval struct {
	TaskID        string
	DeviceName    string
	Action        string
	WorkspacePath string
	Unrestricted  bool
	Arguments     map[string]interface{}
}

type MessageChannelAssistantRequest struct {
	Context                        context.Context
	ModelName                      string
	UserChannelID                  uint
	Messages                       []ChatExecutorMessage
	System                         string
	ConnectorDeviceID              string
	ConnectorWorkspacePath         string
	ConnectorWorkspaceUnrestricted bool
	ConnectorAutoApprove           bool
	ConnectorCommandPrefixes       []string
	AgentGroupID                   string
	MaxTokens                      int
	Temperature                    *float64
	RunID                          string
	OnApprovalRequired             func(context.Context, MessageChannelConnectorApproval) error
}

type MessageChannelAssistantResult struct{ Content string }

func ExecuteMessageChannelAssistantCompletion(*model.User, MessageChannelAssistantRequest) (*MessageChannelAssistantResult, error) {
	return nil, ErrRetiredOnlineChat
}

func DecideMessageChannelConnectorApproval(uint, string, bool) (string, bool, error) {
	return "", false, ErrRetiredOnlineChat
}

func MessageChannelApprovalArgumentsPreview(map[string]interface{}) string { return "" }

func RunMessageChannelConnectorAction(context.Context, uint, string, string, string, bool, string, map[string]interface{}, bool, []string) (string, error) {
	return "", ErrRetiredOnlineChat
}

func ReadAdvancedChatFileData(AdvancedChatFile) ([]byte, error) {
	return nil, ErrRetiredOnlineChat
}

func StoreAdvancedChatPoolFile(uint, string, string, []byte) (AdvancedChatFile, int64, string, error) {
	return AdvancedChatFile{}, 0, "", ErrRetiredOnlineChat
}

func CreateEnterpriseConnectorToken(_ interface{}, _ uint, _ string, _ string, _ int) (AdvancedChatConnectorDevice, string, error) {
	return AdvancedChatConnectorDevice{}, "", ErrRetiredOnlineChat
}

func RotateEnterpriseConnectorToken(_ interface{}, _ string) (AdvancedChatConnectorDevice, string, error) {
	return AdvancedChatConnectorDevice{}, "", ErrRetiredOnlineChat
}
