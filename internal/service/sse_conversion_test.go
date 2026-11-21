package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func conversionFixture(p proxyProtocol) string {
	switch p {
	case protocolOpenAI:
		return `data: {"choices":[{"index":0,"delta":{"content":"hello "}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"content":"world","tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":200,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":50}}}

data: [DONE]

`
	case protocolClaude:
		return `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":150,"output_tokens":1,"cache_read_input_tokens":50}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello world"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_123","name":"lookup","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}

event: message_stop
data: {"type":"message_stop"}

`
	default:
		return `event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"delta":"hello world"}

data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_123","call_id":"call_123","name":"lookup","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"q\":\"x\"}"}

data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_123","call_id":"call_123","name":"lookup","arguments":"{\"q\":\"x\"}"}}

data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello world"}]},{"type":"function_call","call_id":"call_123","name":"lookup","arguments":"{\"q\":\"x\"}"}],"usage":{"input_tokens":200,"output_tokens":30,"input_tokens_details":{"cached_tokens":50}}}}

`
	}
}

type conversionFlushWriter struct {
	bytes.Buffer
	flushes int
}

func (w *conversionFlushWriter) Flush() { w.flushes++ }

func TestProviderSSEConversionAllDirections(t *testing.T) {
	protocols := []proxyProtocol{protocolOpenAI, protocolResponses, protocolClaude}
	for _, up := range protocols {
		for _, down := range protocols {
			if up == down {
				continue
			}
			t.Run(string(up)+"_to_"+string(down), func(t *testing.T) {
				var w conversionFlushWriter
				usage, ok, err := convertProviderSSE(&w, strings.NewReader(conversionFixture(up)), up, down, "model-test")
				if err != nil {
					t.Fatal(err)
				}
				if !ok || usage.InputTokens != 200 || usage.OutputTokens != 30 || usage.CachedInputTokens != 50 {
					t.Fatalf("raw usage lost: %+v, ok=%v", usage, ok)
				}
				if w.flushes < 4 {
					t.Fatalf("events not individually flushed: %d", w.flushes)
				}
				result, convertedUsage, convertedOK, err := readServerChatStream(strings.NewReader(w.String()), down, nil)
				if err != nil {
					t.Fatal(err)
				}
				if result.Content != "hello world" {
					t.Fatalf("text=%q", result.Content)
				}
				if len(result.ToolCalls) != 1 {
					t.Fatalf("tools=%+v", result.ToolCalls)
				}
				tool := result.ToolCalls[0]
				if tool.ID != "call_123" || tool.Name != "lookup" || tool.Arguments != `{"q":"x"}` {
					t.Fatalf("tool metadata/arguments lost: %+v", tool)
				}
				if !convertedOK || convertedUsage.InputTokens != 200 || convertedUsage.OutputTokens != 30 {
					t.Fatalf("downstream usage=%+v", convertedUsage)
				}
				terminal := map[proxyProtocol]string{protocolOpenAI: "data: [DONE]", protocolResponses: "response.completed", protocolClaude: "message_stop"}[down]
				if !strings.Contains(w.String(), terminal) {
					t.Fatalf("missing terminal %s", terminal)
				}
			})
		}
	}
}

func TestProviderSSEMultiLineData(t *testing.T) {
	body := ": keepalive\r\nevent: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"hello\"}\r\n\r\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\r\n\r\n"
	var out bytes.Buffer
	_, _, err := convertProviderSSE(&out, strings.NewReader(body), protocolResponses, protocolClaude, "model")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"text":"hello"`) {
		t.Fatalf("lost multi-line event: %s", out.String())
	}
}

func TestProviderSSEMissingTerminalAndExplicitFailures(t *testing.T) {
	for _, down := range []proxyProtocol{protocolOpenAI, protocolResponses, protocolClaude} {
		up := protocolResponses
		if down == up {
			up = protocolOpenAI
		}
		for _, body := range []string{`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n", `data: {"type":"error","error":{"message":"tool failed"}}` + "\n\n"} {
			var out bytes.Buffer
			_, _, err := convertProviderSSE(&out, strings.NewReader(body), up, down, "model")
			if err == nil {
				t.Fatal("expected incomplete/error stream to fail")
			}
			if strings.Contains(out.String(), "response.completed") || strings.Contains(out.String(), "message_stop") || strings.Contains(out.String(), "data: [DONE]") {
				t.Fatalf("forged completion: %s", out.String())
			}
			if !strings.Contains(out.String(), "upstream_stream_error") {
				t.Fatalf("no downstream failure event: %s", out.String())
			}
		}
	}
}

type conversionErrorReader struct {
	body *strings.Reader
	err  error
}

func (r *conversionErrorReader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if errors.Is(err, io.EOF) {
		return 0, r.err
	}
	return n, err
}

type conversionErrorWriter struct {
	err    error
	writes int
}

func (w *conversionErrorWriter) Write([]byte) (int, error) { w.writes++; return 0, w.err }

func TestProviderSSEReadAndWriteErrors(t *testing.T) {
	sentinel := errors.New("upstream disconnected")
	var out bytes.Buffer
	_, _, err := convertProviderSSE(&out, &conversionErrorReader{strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n"), sentinel}, protocolResponses, protocolClaude, "model")
	if !errors.Is(err, sentinel) || !strings.Contains(out.String(), `"type":"error"`) {
		t.Fatalf("read error lost: %v %s", err, out.String())
	}
	writer := &conversionErrorWriter{err: sentinel}
	_, _, err = convertProviderSSE(writer, strings.NewReader(conversionFixture(protocolResponses)), protocolResponses, protocolClaude, "model")
	if !errors.Is(err, sentinel) || writer.writes != 1 {
		t.Fatalf("write error lost/retried: %v writes=%d", err, writer.writes)
	}
	out.Reset()
	_, _, err = convertProviderSSE(&out, &conversionErrorReader{strings.NewReader(""), context.Canceled}, protocolResponses, protocolClaude, "model")
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancellation emitted failure: %v %s", err, out.String())
	}
}

func TestProviderSSEMapsIncompleteFinishReason(t *testing.T) {
	var out bytes.Buffer
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
	_, _, err := convertProviderSSE(&out, strings.NewReader(body), protocolOpenAI, protocolResponses, "model")
	if err != nil || strings.Contains(out.String(), `"type":"response.completed"`) || !strings.Contains(out.String(), `"type":"response.incomplete"`) {
		t.Fatalf("length not mapped to incomplete: %v %s", err, out.String())
	}
}

func TestProviderSSERejectsMissingToolMetadata(t *testing.T) {
	var out bytes.Buffer
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	_, _, err := convertProviderSSE(&out, strings.NewReader(body), protocolOpenAI, protocolResponses, "model")
	if err == nil || strings.Contains(out.String(), `"type":"response.completed"`) {
		t.Fatalf("invalid tool completed: %v", err)
	}
}

func TestProviderSSEFragmentedToolNames(t *testing.T) {
	body := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_name","function":{"name":"look","arguments":""}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"up","arguments":"{}"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	for _, down := range []proxyProtocol{protocolResponses, protocolClaude} {
		var out bytes.Buffer
		_, _, err := convertProviderSSE(&out, strings.NewReader(body), protocolOpenAI, down, "model")
		if err != nil {
			t.Fatal(err)
		}
		result, _, _, err := readServerChatStream(strings.NewReader(out.String()), down, nil)
		if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "lookup" {
			t.Fatalf("fragmented tool name lost: %v %+v", err, result)
		}
	}
}

func TestProviderSSEUnknownUsageIsNotZero(t *testing.T) {
	for _, down := range []proxyProtocol{protocolOpenAI, protocolResponses, protocolClaude} {
		up := protocolResponses
		body := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
		if down == up {
			up = protocolClaude
			body = "data: {\"type\":\"message_stop\"}\n\n"
		}
		var out bytes.Buffer
		_, ok, err := convertProviderSSE(&out, strings.NewReader(body), up, down, "model")
		if err != nil || ok {
			t.Fatalf("unknown usage should remain unknown: %v %v", ok, err)
		}
		if strings.Contains(out.String(), `"input_tokens":0`) || strings.Contains(out.String(), `"prompt_tokens":0`) || strings.Contains(out.String(), `"output_tokens":0`) {
			t.Fatalf("fabricated zero usage: %s", out.String())
		}
	}
}

func TestProviderSSEMultipleTextSnapshotsAndFinalOnly(t *testing.T) {
	body := `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"one"}` + "\n\n" +
		`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"one"}` + "\n\n" +
		`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"two"}` + "\n\n" +
		`data: {"type":"response.output_text.done","output_index":1,"content_index":0,"text":"two"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"one"}]},{"type":"message","content":[{"type":"output_text","text":"two"}]},{"type":"message","content":[{"type":"output_text","text":"three"}]}]}}` + "\n\n"
	for _, down := range []proxyProtocol{protocolOpenAI, protocolClaude} {
		var out bytes.Buffer
		_, _, err := convertProviderSSE(&out, strings.NewReader(body), protocolResponses, down, "model")
		if err != nil {
			t.Fatal(err)
		}
		result, _, _, err := readServerChatStream(strings.NewReader(out.String()), down, nil)
		if err != nil || result.Content != "onetwothree" {
			t.Fatalf("snapshot content duplicated/lost: %v %+v", err, result)
		}
	}
}

func TestProviderSSELimitedTerminalMappings(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "content_filter"} {
		body := `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"` + reason + `"}}}` + "\n\n"
		for _, down := range []proxyProtocol{protocolOpenAI, protocolClaude} {
			var out bytes.Buffer
			_, _, err := convertProviderSSE(&out, strings.NewReader(body), protocolResponses, down, "model")
			if err != nil {
				t.Fatal(err)
			}
			expect := `"finish_reason":"length"`
			if reason == "content_filter" {
				expect = `"finish_reason":"content_filter"`
			}
			if down == protocolClaude {
				expect = `"stop_reason":"max_tokens"`
			}
			if !strings.Contains(out.String(), expect) || strings.Contains(out.String(), `"type":"error"`) {
				t.Fatalf("wrong limited terminal: %s", out.String())
			}
		}
	}
}

func TestProviderSSEOutputEventsAreJSON(t *testing.T) {
	var out bytes.Buffer
	_, _, err := convertProviderSSE(&out, strings.NewReader(conversionFixture(protocolClaude)), protocolClaude, protocolResponses, "model")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			var d map[string]interface{}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
				t.Fatal(err)
			}
		}
	}
}
