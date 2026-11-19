package service

import (
	"bytes"
	"encoding/json"
	"strings"
)

// streamUsageMeter reads SSE incrementally, independently of the prefix kept
// for risk checks. A single oversized line must not hide later usage events.
type streamUsageMeter struct {
	line     []byte
	skipLine bool
	usage    streamUsageAccumulator
	output   strings.Builder
}

func (m *streamUsageMeter) consume(chunk []byte) {
	for len(chunk) > 0 {
		part, rest, complete := bytes.Cut(chunk, []byte{'\n'})
		if !m.skipLine {
			if len(part) > maxBufferedStreamBytes-len(m.line) {
				m.line = nil
				m.skipLine = true
			} else if complete && len(m.line) == 0 {
				m.readLine(part)
			} else {
				m.line = append(m.line, part...)
				if complete {
					m.readLine(m.line)
				}
			}
		}
		if complete {
			m.line = m.line[:0]
			m.skipLine = false
		}
		chunk = rest
	}
}

func (m *streamUsageMeter) finish() {
	if !m.skipLine && len(m.line) > 0 {
		m.readLine(m.line)
	}
	m.line = nil
}

func (m *streamUsageMeter) readLine(line []byte) {
	payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	if !ok || bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return
	}
	var data map[string]interface{}
	if json.Unmarshal(payload, &data) != nil {
		return
	}
	m.usage.observe(data)
	// Once upstream usage is available, there is no need to buffer text for an estimate.
	if m.usage.ok {
		m.output.Reset()
		return
	}
	if m.output.Len() < maxBufferedStreamBytes {
		appendStreamBillingText(&m.output, data)
	}
}

func (m *streamUsageMeter) usageOrEstimate(modelName string, request normalizedAIRequest) usageTokenCounts {
	if m.usage.ok {
		return m.usage.counts
	}
	return estimateProxyUsage(modelName, request, m.output.String())
}

func parseUsageTokensFromStream(body []byte) (usageTokenCounts, bool) {
	var meter streamUsageMeter
	meter.consume(body)
	meter.finish()
	return meter.usage.counts, meter.usage.ok
}

type streamUsageAccumulator struct {
	counts usageTokenCounts
	ok     bool
	claude map[string]interface{}
}

func (a *streamUsageAccumulator) observe(data map[string]interface{}) {
	// Claude sends initial input/cache usage under message_start.message.usage
	// and cumulative output usage under message_delta.usage. Do not sum snapshots.
	var partial map[string]interface{}
	switch data["type"] {
	case "message_start":
		message, _ := data["message"].(map[string]interface{})
		partial, _ = message["usage"].(map[string]interface{})
		a.claude = nil
	case "message_delta":
		partial, _ = data["usage"].(map[string]interface{})
	}
	if partial != nil {
		if a.claude == nil {
			a.claude = make(map[string]interface{}, len(partial))
		}
		for key, value := range partial {
			if value != nil {
				a.claude[key] = value
			}
		}
		data = map[string]interface{}{"usage": a.claude}
	}
	if counts, ok := parseUsageTokens(data); ok {
		a.counts, a.ok = counts, true
	}
}

func appendStreamBillingText(output *strings.Builder, data map[string]interface{}) {
	// Count only generated text and tool arguments, not IDs, timestamps,
	// repeated snapshots, or other SSE/JSON framing.
	switch data["type"] {
	case "response.output_text.delta", "response.refusal.delta", "response.reasoning_text.delta", "response.function_call_arguments.delta":
		output.WriteString(stringFromValue(data["delta"]))
	case "content_block_delta":
		delta, _ := data["delta"].(map[string]interface{})
		for _, key := range []string{"text", "thinking", "partial_json"} {
			output.WriteString(stringFromValue(delta[key]))
		}
	}
	choices, _ := data["choices"].([]interface{})
	for _, raw := range choices {
		choice, _ := raw.(map[string]interface{})
		delta, _ := choice["delta"].(map[string]interface{})
		output.WriteString(contentToText(delta["content"]))
		output.WriteString(stringFromValue(delta["reasoning_content"]))
		calls, _ := delta["tool_calls"].([]interface{})
		for _, rawCall := range calls {
			call, _ := rawCall.(map[string]interface{})
			function, _ := call["function"].(map[string]interface{})
			output.WriteString(stringFromValue(function["arguments"]))
		}
	}
	candidates, _ := data["candidates"].([]interface{})
	for _, raw := range candidates {
		candidate, _ := raw.(map[string]interface{})
		content, _ := candidate["content"].(map[string]interface{})
		output.WriteString(geminiPartsText(content["parts"]))
	}
}

func estimatedJSONText(items []interface{}) string {
	if len(items) == 0 {
		return ""
	}
	data, _ := json.Marshal(items)
	return string(data)
}

func estimateProxyUsage(modelName string, request normalizedAIRequest, output string) usageTokenCounts {
	// Estimate model input, not the original HTTP body (which can contain
	// unrelated metadata or media that was not forwarded as model text).
	var input strings.Builder
	input.WriteString(normalizedRequestText(request))
	input.WriteString(estimatedJSONText(request.Tools))
	for _, message := range request.Messages {
		input.WriteString(estimatedJSONText(message.ToolCalls))
	}
	return usageTokenCounts{
		InputTokens:  CountTokens(modelName, input.String()),
		OutputTokens: CountTokens(modelName, output),
	}
}
