package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// ask_user lets the model collect requirements interactively: it presents a
// question with preset options (and optionally free-form input) that the chat
// UI renders as a clickable card. The tool never blocks the completion loop —
// the user's choice arrives as the next user message.
const (
	askUserToolName = "ask_user"

	maxAskUserOptions          = 10
	maxAskUserQuestionChars    = 1000
	maxAskUserOptionLabelChars = 120
	maxAskUserOptionDescChars  = 300
)

type askUserOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type askUserPrompt struct {
	Question    string          `json:"question"`
	Options     []askUserOption `json:"options,omitempty"`
	AllowCustom bool            `json:"allow_custom"`
	MultiSelect bool            `json:"multi_select,omitempty"`
	// Note tells the model what happens next so it ends the turn cleanly.
	Note string `json:"note"`
}

func init() {
	RegisterAdvancedChatRuntimeExtensionHook(askUserRuntimeExtension)
	RegisterAdvancedChatToolHandler(askUserToolName, handleAskUserTool)
}

func askUserRuntimeExtension(_ context.Context, input AdvancedChatRuntimeContext) (AdvancedChatRuntimeExtension, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return AdvancedChatRuntimeExtension{}, nil
	}
	return AdvancedChatRuntimeExtension{
		SystemPrompt: "Collecting user input: when a request is ambiguous or a decision is the user's to make, call ask_user with one clear question and 2-6 preset options (set allow_custom true unless only the listed options are valid). Ask one question per turn. After calling ask_user, end your reply and wait — the user's choice or typed answer arrives as their next message. Do not guess instead of asking, and do not ask when the request is already clear.",
		Tools: []ChatExecutorTool{{
			Name:        askUserToolName,
			Description: "Present the user a question with preset options and optional free-form input to collect their choice or requirements. The answer arrives as the user's next message.",
			Schema: map[string]interface{}{
				"type":     "object",
				"required": []string{"question"},
				"properties": map[string]interface{}{
					"question": map[string]interface{}{"type": "string", "description": "The complete question to show the user."},
					"options": map[string]interface{}{
						"type":        "array",
						"description": "Preset choices (2-6 recommended). Each needs a short label; description explains trade-offs.",
						"items": map[string]interface{}{
							"type":     "object",
							"required": []string{"label"},
							"properties": map[string]interface{}{
								"label":       map[string]interface{}{"type": "string", "description": "Short choice text shown on the button."},
								"description": map[string]interface{}{"type": "string", "description": "Optional explanation of this choice."},
							},
						},
					},
					"allow_custom": map[string]interface{}{"type": "boolean", "description": "Whether the user may type a free-form answer instead. Default true."},
					"multi_select": map[string]interface{}{"type": "boolean", "description": "Whether multiple options may be selected. Default false."},
				},
			},
		}},
	}, nil
}

func handleAskUserTool(_ context.Context, input AdvancedChatToolCallInput) (string, error) {
	question := truncateSessionTaskText(stringFromMap(input.Arguments, "question"), maxAskUserQuestionChars)
	if question == "" {
		return "", errors.New("question is required")
	}
	prompt := askUserPrompt{
		Question:    question,
		AllowCustom: true,
		Note:        "The question has been presented to the user. End your reply now; the answer will arrive as the user's next message.",
	}
	if value, ok := input.Arguments["allow_custom"].(bool); ok {
		prompt.AllowCustom = value
	}
	if value, ok := input.Arguments["multi_select"].(bool); ok {
		prompt.MultiSelect = value
	}
	if rawOptions, ok := input.Arguments["options"].([]interface{}); ok {
		if len(rawOptions) > maxAskUserOptions {
			return "", errors.New("too many options")
		}
		for _, rawOption := range rawOptions {
			item, ok := rawOption.(map[string]interface{})
			if !ok {
				return "", errors.New("each option must be an object with a label")
			}
			label := truncateSessionTaskText(stringFromMap(item, "label"), maxAskUserOptionLabelChars)
			if label == "" {
				return "", errors.New("each option needs a non-empty label")
			}
			prompt.Options = append(prompt.Options, askUserOption{
				Label:       label,
				Description: truncateSessionTaskText(stringFromMap(item, "description"), maxAskUserOptionDescChars),
			})
		}
	}
	if len(prompt.Options) == 0 && !prompt.AllowCustom {
		return "", errors.New("provide options or allow custom input")
	}
	payload, err := json.Marshal(prompt)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}
