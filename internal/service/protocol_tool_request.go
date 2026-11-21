package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

// protocolRequestTools converts only function tools. Native Responses tools
// remain native for Responses; incompatible native tools fail explicitly.
func protocolRequestTools(tools []interface{}, target proxyProtocol) ([]interface{}, error) {
	out := make([]interface{}, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("tool definition must be an object")
		}
		function, nested := tool["function"].(map[string]interface{})
		if !nested {
			function = tool
		}
		name, _ := function["name"].(string)
		kind, _ := tool["type"].(string)
		_, claude := tool["input_schema"]
		if (!nested && !claude && kind != "function") || name == "" {
			if target == protocolResponses {
				out = append(out, tool)
				continue
			}
			return nil, fmt.Errorf("tool type %q cannot be converted to %s", kind, target)
		}
		schema := function["parameters"]
		if claude {
			schema = tool["input_schema"]
		}
		if schema == nil {
			schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		definition := map[string]interface{}{"name": name}
		if description, ok := function["description"]; ok {
			definition["description"] = description
		}
		switch target {
		case protocolClaude:
			definition["input_schema"] = schema
			out = append(out, definition)
		case protocolOpenAI, protocolResponses:
			definition["parameters"] = schema
			if strict, ok := function["strict"]; ok {
				definition["strict"] = strict
			}
			if target == protocolOpenAI {
				out = append(out, map[string]interface{}{"type": "function", "function": definition})
			} else {
				definition["type"] = "function"
				out = append(out, definition)
			}
		default:
			return nil, fmt.Errorf("unsupported tool protocol %s", target)
		}
	}
	return out, nil
}

func protocolRequestToolChoice(choice interface{}, target proxyProtocol) (interface{}, error) {
	if choice == nil {
		return nil, nil
	}
	kind, name := "", ""
	switch typed := choice.(type) {
	case string:
		kind = typed
	case map[string]interface{}:
		kind, _ = typed["type"].(string)
		name, _ = typed["name"].(string)
		if function, ok := typed["function"].(map[string]interface{}); ok {
			name, _ = function["name"].(string)
		}
	default:
		return nil, fmt.Errorf("tool_choice must be a string or object")
	}
	if kind == "any" {
		kind = "required"
	}
	if kind == "tool" {
		kind = "function"
	}
	if kind == "function" && name == "" {
		return nil, fmt.Errorf("named tool_choice requires a tool name")
	}
	switch target {
	case protocolClaude:
		if kind == "required" {
			kind = "any"
		}
		if kind == "function" {
			return map[string]interface{}{"type": "tool", "name": name}, nil
		}
		if kind == "auto" || kind == "any" || kind == "none" {
			return map[string]interface{}{"type": kind}, nil
		}
	case protocolOpenAI, protocolResponses:
		if kind == "function" {
			if target == protocolOpenAI {
				return map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": name}}, nil
			}
			return map[string]interface{}{"type": "function", "name": name}, nil
		}
		if kind == "auto" || kind == "required" || kind == "none" {
			return kind, nil
		}
		// Responses has native tool choices beyond function tools.
		if target == protocolResponses {
			return choice, nil
		}
	}
	return nil, fmt.Errorf("tool_choice %q cannot be converted to %s", kind, target)
}

// normalizeClaudeToolHistory stores calls in the canonical Chat representation.
// Tool results become role=tool with the original tool_use_id, including empty
// results. It deliberately treats all tool text and input as opaque data.
func normalizeClaudeToolHistory(messages []interface{}) []normalizedAIMessage {
	var out []normalizedAIMessage
	for _, raw := range messages {
		message, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		blocks, ok := message["content"].([]interface{})
		if !ok {
			out = append(out, normalizedAIMessage{Role: role, Content: contentToText(message["content"])})
			continue
		}
		pending := normalizedAIMessage{Role: role}
		flush := func() {
			if pending.Content != "" || len(pending.ToolCalls) > 0 {
				out = append(out, pending)
				pending = normalizedAIMessage{Role: role}
			}
		}
		for _, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]interface{})
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				text, _ := block["text"].(string)
				if pending.Content != "" {
					pending.Content += "\n"
				}
				pending.Content += text
			case "tool_use":
				args, _ := json.Marshal(block["input"])
				pending.ToolCalls = append(pending.ToolCalls, map[string]interface{}{"id": block["id"], "type": "function", "function": map[string]interface{}{"name": block["name"], "arguments": string(args)}})
			case "tool_result":
				flush()
				id, _ := block["tool_use_id"].(string)
				out = append(out, normalizedAIMessage{Role: "tool", ToolCallID: id, Content: protocolToolResultText(block["content"])})
			}
		}
		flush()
	}
	return out
}

func protocolToolResultText(value interface{}) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	if blocks, ok := value.([]interface{}); ok {
		texts := make([]string, 0, len(blocks))
		allText := true
		for _, raw := range blocks {
			block, ok := raw.(map[string]interface{})
			if !ok || block["type"] != "text" {
				allText = false
				break
			}
			text, _ := block["text"].(string)
			texts = append(texts, text)
		}
		if allText {
			return strings.Join(texts, "\n")
		}
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func protocolToolCallFields(raw interface{}) (string, string, string, error) {
	call, ok := raw.(map[string]interface{})
	if !ok {
		return "", "", "", fmt.Errorf("tool call must be an object")
	}
	function, ok := call["function"].(map[string]interface{})
	if !ok {
		return "", "", "", fmt.Errorf("normalized tool call requires function")
	}
	id, _ := call["id"].(string)
	name, _ := function["name"].(string)
	if id == "" || name == "" {
		return "", "", "", fmt.Errorf("tool call requires id and name")
	}
	arguments, ok := function["arguments"].(string)
	if !ok {
		encoded, err := json.Marshal(function["arguments"])
		if err != nil {
			return "", "", "", err
		}
		arguments = string(encoded)
	}
	return id, name, arguments, nil
}

// Use this for the fallback in openAIResponsesPayloadMap when ResponsesInput is
// absent. Responses history uses separate function_call/function_call_output
// items, never Chat's tool_calls or role=tool.
func protocolResponsesToolHistory(request normalizedAIRequest) ([]map[string]interface{}, error) {
	out := make([]map[string]interface{}, 0, len(request.Messages)+1)
	if request.System != "" {
		out = append(out, map[string]interface{}{"role": "system", "content": request.System})
	}
	for _, message := range request.Messages {
		if message.Role == "tool" {
			if message.ToolCallID == "" {
				return nil, fmt.Errorf("tool result requires tool_call_id")
			}
			out = append(out, map[string]interface{}{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content})
			continue
		}
		if message.Content != "" || len(message.ToolCalls) == 0 {
			out = append(out, map[string]interface{}{"role": message.Role, "content": message.Content})
		}
		for _, raw := range message.ToolCalls {
			id, name, args, err := protocolToolCallFields(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]interface{}{"type": "function_call", "call_id": id, "name": name, "arguments": args})
		}
	}
	return out, nil
}

func protocolClaudeToolHistory(messages []normalizedAIMessage) ([]map[string]interface{}, error) {
	out := make([]map[string]interface{}, 0, len(messages))
	for _, message := range messages {
		role := "user"
		if message.Role == "assistant" {
			role = "assistant"
		}
		content := make([]interface{}, 0, 1+len(message.ToolCalls))
		if message.Role == "tool" {
			if message.ToolCallID == "" {
				return nil, fmt.Errorf("tool result requires tool_call_id")
			}
			content = append(content, map[string]interface{}{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": message.Content})
		} else {
			if message.Content != "" {
				content = append(content, map[string]interface{}{"type": "text", "text": message.Content})
			}
			for _, raw := range message.ToolCalls {
				id, name, args, err := protocolToolCallFields(raw)
				if err != nil {
					return nil, err
				}
				var input map[string]interface{}
				if err = json.Unmarshal([]byte(args), &input); err != nil || input == nil {
					return nil, fmt.Errorf("tool call %s arguments must be a JSON object", id)
				}
				content = append(content, map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": input})
			}
		}
		if len(content) == 0 {
			continue
		}
		// Claude requires alternating turns. Merge consecutive tool results and
		// adjacent user content into one user turn without reordering blocks.
		if len(out) > 0 && out[len(out)-1]["role"] == role {
			last := out[len(out)-1]
			last["content"] = append(last["content"].([]interface{}), content...)
		} else {
			out = append(out, map[string]interface{}{"role": role, "content": content})
		}
	}
	return out, nil
}
