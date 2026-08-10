package service

import (
	"context"
	"errors"

	"github.com/veloce-ailab/veloce/internal/model"
)

var ErrRetiredOnlineChat = errors.New("online chat has been removed")

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
