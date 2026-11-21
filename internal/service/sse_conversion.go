package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// convertProviderSSE converts one response (one Chat choice) between Chat,
// Responses and Messages. Usage is observed only on original upstream events.
// The caller owns headers, cancellation, billing and body.Close. Same-protocol
// streams should be forwarded directly, not passed through this converter.
func convertProviderSSE(w io.Writer, body io.Reader, upstream, downstream proxyProtocol, modelName string) (usageTokenCounts, bool, error) {
	c := &providerSSEConverter{w: w, upstream: upstream, downstream: downstream, model: modelName, id: fmt.Sprintf("resp_%d", time.Now().UnixNano()), tools: map[int]*convertedSSETool{}}
	if !conversionProtocolSupported(upstream) || !conversionProtocolSupported(downstream) || upstream == downstream {
		return usageTokenCounts{}, false, fmt.Errorf("unsupported SSE conversion %s -> %s", upstream, downstream)
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxBufferedStreamBytes)
	var event string
	var lines []string
	size := 0
	dispatch := func() error {
		if len(lines) == 0 {
			event = ""
			return nil
		}
		payload := strings.Join(lines, "\n")
		lines = nil
		size = 0
		name := event
		event = ""
		if strings.TrimSpace(payload) == "[DONE]" {
			if upstream != protocolOpenAI || !c.complete {
				return errors.New("upstream stream ended without a finish reason")
			}
			c.terminal = true
			return nil
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return fmt.Errorf("invalid upstream SSE JSON: %w", err)
		}
		if name != "" && data["type"] == nil {
			data["type"] = name
		}
		c.usage.observe(data)
		return c.consume(data)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return c.fail(err)
			}
			if c.terminal {
				return c.finish()
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			size += len(value) + 1
			if size > maxBufferedStreamBytes {
				return c.fail(errors.New("upstream SSE event exceeds size limit"))
			}
			lines = append(lines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return c.fail(err)
	}
	if err := dispatch(); err != nil {
		return c.fail(err)
	}
	if !c.complete {
		return c.fail(errors.New("upstream stream closed before completion"))
	}
	return c.finish()
}

func conversionProtocolSupported(p proxyProtocol) bool {
	return p == protocolOpenAI || p == protocolResponses || p == protocolClaude
}

type convertedSSETool struct {
	id, name, args string
	emitted        bool
	sent           int
	out            int
}
type providerSSEConverter struct {
	w                           io.Writer
	upstream, downstream        proxyProtocol
	model, id, text, reason     string
	started, complete, terminal bool
	incomplete                  bool
	writeErr                    error
	usage                       streamUsageAccumulator
	tools                       map[int]*convertedSSETool
	nextTool                    int
	sequence                    int
	textParts                   map[string]string
}

func (c *providerSSEConverter) emit(event string, data interface{}) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	if m, ok := data.(map[string]interface{}); ok && c.downstream == protocolResponses {
		m["sequence_number"] = c.sequence
		c.sequence++
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	prefix := ""
	if event != "" {
		prefix = "event: " + event + "\n"
	}
	_, err = fmt.Fprintf(c.w, "%sdata: %s\n\n", prefix, encoded)
	if err != nil {
		c.writeErr = err
		return err
	}
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
func (c *providerSSEConverter) chat(delta interface{}, finish interface{}, usage interface{}) map[string]interface{} {
	m := map[string]interface{}{"id": c.id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": c.model, "choices": []interface{}{map[string]interface{}{"index": 0, "delta": delta, "finish_reason": finish}}}
	if usage != nil {
		m["usage"] = usage
	}
	return m
}
func (c *providerSSEConverter) textItem(status string) map[string]interface{} {
	return map[string]interface{}{"id": c.id + "_msg", "type": "message", "role": "assistant", "status": status, "content": []interface{}{map[string]interface{}{"type": "output_text", "text": c.text, "annotations": []interface{}{}}}}
}
func (c *providerSSEConverter) toolItem(t *convertedSSETool, status string) map[string]interface{} {
	return map[string]interface{}{"id": c.id + fmt.Sprintf("_tool_%d", t.out), "type": "function_call", "call_id": t.id, "name": t.name, "arguments": t.args, "status": status}
}
func (c *providerSSEConverter) response(status string) map[string]interface{} {
	output := []interface{}{c.textItem(status)}
	ts := c.sortedTools()
	for _, t := range ts {
		output = append(output, c.toolItem(t, status))
	}
	u := c.usage.counts
	r := map[string]interface{}{"id": c.id, "object": "response", "created_at": time.Now().Unix(), "model": c.model, "status": status, "output": output, "error": nil, "incomplete_details": nil, "usage": nil}
	if c.usage.ok {
		r["usage"] = map[string]interface{}{"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens, "total_tokens": u.InputTokens + u.OutputTokens, "input_tokens_details": map[string]interface{}{"cached_tokens": u.CachedInputTokens}, "cache_creation_input_tokens": u.CacheWriteInputTokens, "cache_creation_1h_input_tokens": u.CacheWrite1hInputTokens}
	}
	return r
}
func (c *providerSSEConverter) start() error {
	if c.started {
		return nil
	}
	c.started = true
	switch c.downstream {
	case protocolOpenAI:
		return c.emit("", c.chat(map[string]interface{}{"role": "assistant", "content": ""}, nil, nil))
	case protocolClaude:
		if err := c.emit("message_start", map[string]interface{}{"type": "message_start", "message": map[string]interface{}{"id": c.id, "type": "message", "role": "assistant", "model": c.model, "content": []interface{}{}, "stop_reason": nil, "stop_sequence": nil, "usage": c.claudeUsage()}}); err != nil {
			return err
		}
		return c.emit("content_block_start", map[string]interface{}{"type": "content_block_start", "index": 0, "content_block": map[string]interface{}{"type": "text", "text": ""}})
	case protocolResponses:
		r := c.response("in_progress")
		r["output"] = []interface{}{}
		if err := c.emit("response.created", map[string]interface{}{"type": "response.created", "response": r}); err != nil {
			return err
		}
		if err := c.emit("response.output_item.added", map[string]interface{}{"type": "response.output_item.added", "output_index": 0, "item": c.textItem("in_progress")}); err != nil {
			return err
		}
		return c.emit("response.content_part.added", map[string]interface{}{"type": "response.content_part.added", "item_id": c.id + "_msg", "output_index": 0, "content_index": 0, "part": map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}}})
	}
	return nil
}
func (c *providerSSEConverter) appendText(s string) error {
	if s == "" {
		return nil
	}
	if err := c.start(); err != nil {
		return err
	}
	c.text += s
	switch c.downstream {
	case protocolOpenAI:
		return c.emit("", c.chat(map[string]interface{}{"content": s}, nil, nil))
	case protocolClaude:
		return c.emit("content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": 0, "delta": map[string]interface{}{"type": "text_delta", "text": s}})
	default:
		return c.emit("response.output_text.delta", map[string]interface{}{"type": "response.output_text.delta", "item_id": c.id + "_msg", "output_index": 0, "content_index": 0, "delta": s})
	}
}
func (c *providerSSEConverter) tool(index int, id, name, args string, snapshot bool) error {
	t := c.tools[index]
	if t == nil {
		t = &convertedSSETool{}
		c.tools[index] = t
	}
	if id != "" {
		t.id = id
	}
	if name != "" {
		if snapshot || c.upstream != protocolOpenAI {
			t.name = name
		} else {
			t.name += name
		}
	}
	if snapshot {
		if args != "" {
			if !strings.HasPrefix(args, t.args) {
				return errors.New("inconsistent upstream tool arguments snapshot")
			}
			t.args = args
		}
	} else {
		t.args += args
	}
	if t.id == "" || t.name == "" {
		return nil
	}
	// Chat may split the function name across chunks. Wait until arguments
	// begin (or the final flush) before opening a Claude/Responses tool item.
	if c.upstream == protocolOpenAI && !snapshot && t.args == "" && !t.emitted {
		return nil
	}
	if err := c.start(); err != nil {
		return err
	}
	if !t.emitted {
		t.emitted = true
		t.out = c.nextTool + 1
		c.nextTool++
		switch c.downstream {
		case protocolOpenAI:
			if err := c.emit("", c.chat(map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"index": t.out - 1, "id": t.id, "type": "function", "function": map[string]interface{}{"name": t.name, "arguments": ""}}}}, nil, nil)); err != nil {
				return err
			}
		case protocolClaude:
			if err := c.emit("content_block_start", map[string]interface{}{"type": "content_block_start", "index": t.out, "content_block": map[string]interface{}{"type": "tool_use", "id": t.id, "name": t.name, "input": map[string]interface{}{}}}); err != nil {
				return err
			}
		case protocolResponses:
			item := c.toolItem(t, "in_progress")
			item["arguments"] = ""
			if err := c.emit("response.output_item.added", map[string]interface{}{"type": "response.output_item.added", "output_index": t.out, "item": item}); err != nil {
				return err
			}
		}
	}
	delta := t.args[t.sent:]
	t.sent = len(t.args)
	if delta == "" {
		return nil
	}
	switch c.downstream {
	case protocolOpenAI:
		return c.emit("", c.chat(map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"index": t.out - 1, "function": map[string]interface{}{"arguments": delta}}}}, nil, nil))
	case protocolClaude:
		return c.emit("content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": t.out, "delta": map[string]interface{}{"type": "input_json_delta", "partial_json": delta}})
	default:
		return c.emit("response.function_call_arguments.delta", map[string]interface{}{"type": "response.function_call_arguments.delta", "item_id": c.id + fmt.Sprintf("_tool_%d", t.out), "output_index": t.out, "delta": delta})
	}
}

func (c *providerSSEConverter) responseText(index, contentIndex int, text string, snapshot bool) error {
	if c.textParts == nil {
		c.textParts = map[string]string{}
	}
	key := fmt.Sprintf("%d:%d", index, contentIndex)
	if snapshot {
		old := c.textParts[key]
		if !strings.HasPrefix(text, old) {
			return errors.New("inconsistent upstream text snapshot")
		}
		text = strings.TrimPrefix(text, old)
	}
	c.textParts[key] += text
	return c.appendText(text)
}

func (c *providerSSEConverter) consume(d map[string]interface{}) error {
	typ := stringFromValue(d["type"])
	if d["error"] != nil || typ == "error" || typ == "response.failed" {
		return fmt.Errorf("upstream SSE failure (%s)", typ)
	}
	switch c.upstream {
	case protocolOpenAI:
		choices, _ := d["choices"].([]interface{})
		if len(choices) > 1 {
			return errors.New("SSE conversion supports only one Chat choice")
		}
		for _, raw := range choices {
			choice, _ := raw.(map[string]interface{})
			if intFromStreamValue(choice["index"]) != 0 {
				return errors.New("SSE conversion supports only Chat choice zero")
			}
			delta, _ := choice["delta"].(map[string]interface{})
			if err := c.appendText(contentToText(delta["content"])); err != nil {
				return err
			}
			calls, _ := delta["tool_calls"].([]interface{})
			for _, rawCall := range calls {
				call, _ := rawCall.(map[string]interface{})
				fn, _ := call["function"].(map[string]interface{})
				if err := c.tool(intFromStreamValue(call["index"]), stringFromValue(call["id"]), stringFromValue(fn["name"]), stringFromValue(fn["arguments"]), false); err != nil {
					return err
				}
			}
			if reason := stringFromValue(choice["finish_reason"]); reason != "" {
				c.complete = true
				c.reason = reason
			}
		}
	case protocolClaude:
		index := intFromStreamValue(d["index"])
		switch typ {
		case "content_block_start":
			block, _ := d["content_block"].(map[string]interface{})
			if block["type"] == "tool_use" {
				args := ""
				if input, ok := block["input"].(map[string]interface{}); ok && len(input) > 0 {
					b, _ := json.Marshal(input)
					args = string(b)
				}
				return c.tool(index, stringFromValue(block["id"]), stringFromValue(block["name"]), args, true)
			}
			if block["type"] == "text" {
				return c.appendText(stringFromValue(block["text"]))
			}
		case "content_block_delta":
			delta, _ := d["delta"].(map[string]interface{})
			switch delta["type"] {
			case "text_delta":
				return c.appendText(stringFromValue(delta["text"]))
			case "input_json_delta":
				return c.tool(index, "", "", stringFromValue(delta["partial_json"]), false)
			}
		case "message_delta":
			delta, _ := d["delta"].(map[string]interface{})
			c.reason = stringFromValue(delta["stop_reason"])
		case "message_stop":
			c.complete = true
			c.terminal = true
		}
	case protocolResponses:
		index := intFromStreamValue(d["output_index"], d["index"])
		switch typ {
		case "response.output_text.delta", "response.refusal.delta":
			return c.responseText(index, intFromStreamValue(d["content_index"]), stringFromValue(d["delta"]), false)
		case "response.output_text.done":
			return c.responseText(index, intFromStreamValue(d["content_index"]), stringFromValue(d["text"]), true)
		case "response.refusal.done":
			return c.responseText(index, intFromStreamValue(d["content_index"]), stringFromValue(d["refusal"]), true)
		case "response.output_item.added", "response.output_item.done":
			item, _ := d["item"].(map[string]interface{})
			if item["type"] == "function_call" {
				return c.tool(index, stringFromValue(item["call_id"]), stringFromValue(item["name"]), stringFromValue(item["arguments"]), true)
			}
		case "response.function_call_arguments.delta":
			return c.tool(index, "", "", stringFromValue(d["delta"]), false)
		case "response.function_call_arguments.done":
			return c.tool(index, stringFromValue(d["call_id"]), stringFromValue(d["name"]), stringFromValue(d["arguments"]), true)
		case "response.completed", "response.incomplete":
			response, _ := d["response"].(map[string]interface{})
			if status := stringFromValue(response["status"]); status != "" && status != "completed" && status != "incomplete" {
				return fmt.Errorf("upstream response status %s", status)
			}
			if typ == "response.incomplete" || response["status"] == "incomplete" {
				c.incomplete = true
				details, _ := response["incomplete_details"].(map[string]interface{})
				c.reason = stringFromValue(details["reason"])
				if c.reason == "" {
					c.reason = "max_output_tokens"
				}
			}
			items, _ := response["output"].([]interface{})
			for i, raw := range items {
				item, _ := raw.(map[string]interface{})
				if item["type"] == "function_call" {
					if err := c.tool(i, stringFromValue(item["call_id"]), stringFromValue(item["name"]), stringFromValue(item["arguments"]), true); err != nil {
						return err
					}
				}
			}
			for i, raw := range items {
				item, _ := raw.(map[string]interface{})
				if item["type"] == "message" {
					content, _ := item["content"].([]interface{})
					for j, rawPart := range content {
						part, _ := rawPart.(map[string]interface{})
						text := ""
						switch part["type"] {
						case "output_text":
							text = stringFromValue(part["text"])
						case "refusal":
							text = stringFromValue(part["refusal"])
						default:
							continue
						}
						if err := c.responseText(i, j, text, true); err != nil {
							return err
						}
					}
				}
			}
			c.complete = true
			c.terminal = true
		}
	}
	return nil
}
func (c *providerSSEConverter) sortedTools() []*convertedSSETool {
	ts := make([]*convertedSSETool, 0, len(c.tools))
	for _, t := range c.tools {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].out < ts[j].out })
	return ts
}
func (c *providerSSEConverter) claudeUsage() map[string]interface{} {
	if !c.usage.ok {
		return nil
	}
	u := c.usage.counts
	n := u.InputTokens - u.CacheReadInputTokens - u.CacheWriteInputTokens - u.CacheWrite1hInputTokens
	if n < 0 {
		n = 0
	}
	return map[string]interface{}{"input_tokens": n, "output_tokens": u.OutputTokens, "cache_read_input_tokens": u.CacheReadInputTokens, "cache_creation_input_tokens": u.CacheWriteInputTokens, "cache_creation_1h_input_tokens": u.CacheWrite1hInputTokens}
}
func (c *providerSSEConverter) finish() (usageTokenCounts, bool, error) {
	indexes := make([]int, 0, len(c.tools))
	for index := range c.tools {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		t := c.tools[index]
		if t.id == "" || t.name == "" {
			return c.fail(errors.New("upstream tool call missing ID or name"))
		}
		if !t.emitted {
			if err := c.tool(index, t.id, t.name, t.args, true); err != nil {
				return c.fail(err)
			}
		}
	}
	if err := c.start(); err != nil {
		return c.usage.counts, c.usage.ok, err
	}
	reason := c.reason
	limited := c.incomplete || reason == "length" || reason == "max_tokens" || reason == "max_output_tokens" || reason == "content_filter"
	var err error
	switch c.downstream {
	case protocolOpenAI:
		reason = "stop"
		if len(c.tools) > 0 {
			reason = "tool_calls"
		}
		if limited {
			reason = "length"
			if c.reason == "content_filter" {
				reason = "content_filter"
			}
		}
		u := c.usage.counts
		var usage interface{}
		if c.usage.ok {
			usage = map[string]interface{}{"prompt_tokens": u.InputTokens, "completion_tokens": u.OutputTokens, "total_tokens": u.InputTokens + u.OutputTokens, "prompt_tokens_details": map[string]interface{}{"cached_tokens": u.CachedInputTokens}, "cache_creation_input_tokens": u.CacheWriteInputTokens, "cache_creation_1h_input_tokens": u.CacheWrite1hInputTokens}
		}
		err = c.emit("", c.chat(map[string]interface{}{}, reason, usage))
		if err == nil {
			_, err = io.WriteString(c.w, "data: [DONE]\n\n")
			if err != nil {
				c.writeErr = err
			}
			if f, ok := c.w.(http.Flusher); ok && err == nil {
				f.Flush()
			}
		}
	case protocolClaude:
		err = c.emit("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0})
		for _, t := range c.sortedTools() {
			if err == nil {
				err = c.emit("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": t.out})
			}
		}
		reason = "end_turn"
		if len(c.tools) > 0 {
			reason = "tool_use"
		}
		if limited {
			reason = "max_tokens"
		}
		if err == nil {
			err = c.emit("message_delta", map[string]interface{}{"type": "message_delta", "delta": map[string]interface{}{"stop_reason": reason, "stop_sequence": nil}, "usage": c.claudeUsage()})
		}
		if err == nil {
			err = c.emit("message_stop", map[string]interface{}{"type": "message_stop"})
		}
	case protocolResponses:
		err = c.emit("response.output_text.done", map[string]interface{}{"type": "response.output_text.done", "item_id": c.id + "_msg", "output_index": 0, "content_index": 0, "text": c.text})
		if err == nil {
			err = c.emit("response.content_part.done", map[string]interface{}{"type": "response.content_part.done", "item_id": c.id + "_msg", "output_index": 0, "content_index": 0, "part": map[string]interface{}{"type": "output_text", "text": c.text, "annotations": []interface{}{}}})
		}
		if err == nil {
			err = c.emit("response.output_item.done", map[string]interface{}{"type": "response.output_item.done", "output_index": 0, "item": c.textItem("completed")})
		}
		for _, t := range c.sortedTools() {
			if err == nil {
				err = c.emit("response.function_call_arguments.done", map[string]interface{}{"type": "response.function_call_arguments.done", "item_id": c.id + fmt.Sprintf("_tool_%d", t.out), "output_index": t.out, "name": t.name, "arguments": t.args})
			}
			if err == nil {
				err = c.emit("response.output_item.done", map[string]interface{}{"type": "response.output_item.done", "output_index": t.out, "item": c.toolItem(t, "completed")})
			}
		}
		if err == nil {
			if limited {
				r := c.response("incomplete")
				incompleteReason := "max_output_tokens"
				if c.reason == "content_filter" {
					incompleteReason = "content_filter"
				}
				r["incomplete_details"] = map[string]interface{}{"reason": incompleteReason}
				err = c.emit("response.incomplete", map[string]interface{}{"type": "response.incomplete", "response": r})
			} else {
				err = c.emit("response.completed", map[string]interface{}{"type": "response.completed", "response": c.response("completed")})
			}
		}
	}
	return c.usage.counts, c.usage.ok, err
}
func (c *providerSSEConverter) fail(err error) (usageTokenCounts, bool, error) {
	if c.writeErr != nil {
		return c.usage.counts, c.usage.ok, c.writeErr
	}
	if errors.Is(err, context.Canceled) {
		return c.usage.counts, c.usage.ok, err
	}
	detail := map[string]interface{}{"type": "upstream_stream_error", "message": err.Error()}
	var writeErr error
	switch c.downstream {
	case protocolResponses:
		r := c.response("failed")
		r["error"] = detail
		writeErr = c.emit("response.failed", map[string]interface{}{"type": "response.failed", "response": r})
	case protocolClaude:
		writeErr = c.emit("error", map[string]interface{}{"type": "error", "error": detail})
	default:
		writeErr = c.emit("error", map[string]interface{}{"error": detail})
	}
	if writeErr != nil {
		return c.usage.counts, c.usage.ok, writeErr
	}
	return c.usage.counts, c.usage.ok, err
}
