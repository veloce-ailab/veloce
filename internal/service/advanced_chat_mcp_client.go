package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// mcpProtocolVersion is the MCP protocol version this client negotiates. MCP
// servers that support a different version echo their own in the initialize
// result; we send a recent stable version and tolerate the server's choice.
const mcpProtocolVersion = "2025-06-18"

const (
	mcpRequestTimeout = 30 * time.Second
	mcpMaxResponse    = 4 << 20 // 4 MiB cap on a single JSON-RPC response body
)

// mcpTool is a tool advertised by an MCP server via tools/list.
type mcpTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// mcpToolResult is the normalized text output of a tools/call invocation.
type mcpToolResult struct {
	Text    string
	IsError bool
}

type mcpToolClient interface {
	listTools(ctx context.Context) ([]mcpTool, error)
	callTool(ctx context.Context, name string, arguments map[string]interface{}) (mcpToolResult, error)
}

// mcpClient talks to a single MCP server over Streamable HTTP: every request is
// an HTTP POST of a JSON-RPC message to the server URL, and the response is
// either a JSON object or an SSE stream carrying the JSON-RPC response. The
// client keeps the negotiated session id (if the server issues one) so follow-up
// calls on the same client are associated with the same session.
type mcpClient struct {
	url         string
	headers     map[string]string
	httpClient  *http.Client
	sessionID   string
	idCounter   int64
	initialized bool
}

func newMCPClient(url string, headers map[string]string) *mcpClient {
	return &mcpClient{
		url:        strings.TrimSpace(url),
		headers:    headers,
		httpClient: &http.Client{Timeout: mcpRequestTimeout, CheckRedirect: GuardedRedirectPolicy()},
	}
}

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonRPCNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error"`
}

type jsonRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message)
}

func (client *mcpClient) nextID() int64 {
	return atomic.AddInt64(&client.idCounter, 1)
}

// initialize performs the MCP initialize handshake and sends the
// notifications/initialized acknowledgement. It is idempotent.
func (client *mcpClient) initialize(ctx context.Context) error {
	if client.initialized {
		return nil
	}
	params := map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "windypear-advanced-chat",
			"version": "1.0.0",
		},
	}
	if _, err := client.call(ctx, "initialize", params); err != nil {
		return err
	}
	if err := client.notify(ctx, "notifications/initialized", nil); err != nil {
		return err
	}
	client.initialized = true
	return nil
}

// listTools returns the tools advertised by the server.
func (client *mcpClient) listTools(ctx context.Context) ([]mcpTool, error) {
	if err := client.initialize(ctx); err != nil {
		return nil, err
	}
	result, err := client.call(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("failed to decode tools/list: %w", err)
	}
	return payload.Tools, nil
}

// callTool invokes a tool and flattens the content blocks into a single string.
func (client *mcpClient) callTool(ctx context.Context, name string, arguments map[string]interface{}) (mcpToolResult, error) {
	if err := client.initialize(ctx); err != nil {
		return mcpToolResult{}, err
	}
	if arguments == nil {
		arguments = map[string]interface{}{}
	}
	result, err := client.call(ctx, "tools/call", map[string]interface{}{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return mcpToolResult{}, err
	}
	return parseMCPToolCallResult(result)
}

func parseMCPToolCallResult(result json.RawMessage) (mcpToolResult, error) {
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return mcpToolResult{}, fmt.Errorf("failed to decode tools/call: %w", err)
	}
	parts := make([]string, 0, len(payload.Content))
	for _, block := range payload.Content {
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if text == "" && len(payload.StructuredContent) > 0 {
		text = string(payload.StructuredContent)
	}
	return mcpToolResult{Text: text, IsError: payload.IsError}, nil
}

// call sends a JSON-RPC request and returns the raw result payload.
func (client *mcpClient) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	request := jsonRPCRequest{JSONRPC: "2.0", ID: client.nextID(), Method: method, Params: params}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	response, err := client.post(ctx, body, true)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("mcp server returned no response for %s", method)
	}
	if response.Error != nil {
		return nil, response.Error
	}
	return response.Result, nil
}

// notify sends a JSON-RPC notification (no id, no response expected).
func (client *mcpClient) notify(ctx context.Context, method string, params interface{}) error {
	notification := jsonRPCNotification{JSONRPC: "2.0", Method: method, Params: params}
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	_, err = client.post(ctx, body, false)
	return err
}

// post sends one JSON-RPC message and, when expectResponse is true, parses the
// matching JSON-RPC response out of either a JSON body or an SSE stream.
func (client *mcpClient) post(ctx context.Context, body []byte, expectResponse bool) (*jsonRPCResponse, error) {
	if client.url == "" {
		return nil, errors.New("mcp server url is empty")
	}
	if err := ValidateConfiguredHTTPURL(client.url); err != nil {
		return nil, fmt.Errorf("mcp server url blocked: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if client.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", client.sessionID)
	}
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	for key, value := range client.headers {
		if strings.TrimSpace(key) == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if sessionID := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); sessionID != "" {
		client.sessionID = sessionID
	}

	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode >= http.StatusBadRequest {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("mcp server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	if !expectResponse {
		return nil, nil
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return readJSONRPCFromSSE(resp.Body, requestID(body))
	}
	limited := io.LimitReader(resp.Body, mcpMaxResponse)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	return parseJSONRPCResponse(raw)
}

// requestID extracts the JSON-RPC id from an outgoing request so SSE parsing can
// match the corresponding response.
func requestID(body []byte) int64 {
	var probe struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.ID
}

func parseJSONRPCResponse(raw []byte) (*jsonRPCResponse, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errors.New("empty mcp response")
	}
	// A server may batch responses in an array; take the first object that has a result/error.
	if raw[0] == '[' {
		var batch []jsonRPCResponse
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, err
		}
		for i := range batch {
			if batch[i].Result != nil || batch[i].Error != nil {
				return &batch[i], nil
			}
		}
		return nil, errors.New("mcp batch response had no result")
	}
	var response jsonRPCResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// readJSONRPCFromSSE consumes an SSE stream and returns the first JSON-RPC
// response whose id matches the request (or the first response carrying a
// result/error when ids cannot be compared).
func readJSONRPCFromSSE(body io.Reader, wantID int64) (*jsonRPCResponse, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, mcpMaxResponse))
	scanner.Buffer(make([]byte, 0, 64*1024), mcpMaxResponse)
	var dataLines []string
	flush := func() (*jsonRPCResponse, bool, error) {
		if len(dataLines) == 0 {
			return nil, false, nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(payload) == "" {
			return nil, false, nil
		}
		response, err := parseJSONRPCResponse([]byte(payload))
		if err != nil {
			return nil, false, nil // ignore non-JSON-RPC events (pings, etc.)
		}
		if response.Result == nil && response.Error == nil {
			return nil, false, nil
		}
		if wantID != 0 {
			var id int64
			if err := json.Unmarshal(response.ID, &id); err == nil && id != wantID {
				return nil, false, nil
			}
		}
		return response, true, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			response, done, err := flush()
			if err != nil {
				return nil, err
			}
			if done {
				return response, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if response, done, err := flush(); err != nil {
		return nil, err
	} else if done {
		return response, nil
	}
	return nil, errors.New("mcp sse stream ended without a response")
}
