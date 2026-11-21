package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/veloce-ailab/veloce/internal/config"
)

func TestProxyUpstreamDeadlineAllowsLongGenerations(t *testing.T) {
	client := newProxyUpstreamHTTPClient()
	transport := client.Transport.(*http.Transport)
	defer transport.CloseIdleConnections()
	if client.Timeout <= 120*time.Second {
		t.Fatalf("total deadline must allow 90-120 second generations, got %v", client.Timeout)
	}
	if transport.ResponseHeaderTimeout != time.Duration(config.UpstreamResponseHeaderTimeoutSeconds)*time.Second {
		t.Fatal("missing header deadline")
	}
}

func TestProxyUpstreamHonorsClientCancellationAfterHeaders(t *testing.T) {
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(stopped)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := newProxyUpstreamHTTPClient()
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive cancellation")
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("expected canceled body read")
	}
}

func TestProxyReasoningParametersSurviveNormalization(t *testing.T) {
	for _, effort := range []string{"low", "high"} {
		t.Run(effort, func(t *testing.T) {
			req := normalizeOpenAIRequest("/v1/responses", map[string]interface{}{
				"input": "hello", "stream": true,
				"reasoning": map[string]interface{}{"effort": effort, "summary": "auto"},
			}, "model")
			payload, err := openAIResponsesPayloadMap(req)
			if err != nil {
				t.Fatal(err)
			}
			reasoning, _ := payload["reasoning"].(map[string]interface{})
			if reasoning["effort"] != effort || reasoning["summary"] != "auto" {
				t.Fatalf("reasoning lost: %v", payload)
			}
			chat, err := openAIChatCompletionsPayloadMap(req)
			if err != nil {
				t.Fatal(err)
			}
			if chat["reasoning_effort"] != effort {
				t.Fatalf("chat effort lost: %v", chat)
			}
			req = normalizeOpenAIRequest("/v1/chat/completions", map[string]interface{}{
				"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hello"}}, "reasoning_effort": effort,
			}, "model")
			payload, err = openAIResponsesPayloadMap(req)
			if err != nil {
				t.Fatal(err)
			}
			reasoning, _ = payload["reasoning"].(map[string]interface{})
			if reasoning["effort"] != effort {
				t.Fatalf("converted effort lost: %v", payload)
			}
		})
	}
}

// Opt-in wall-clock reproduction: a controlled upstream still produces data
// beyond the old 60-second deadline. No external model calls or token charges.
func TestProxyLongStreamBeyondSixtySeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("long-running test")
	}
	if !strings.EqualFold(os.Getenv("VELOCE_TEST_LONG_STREAM"), "true") {
		t.Skip("set VELOCE_TEST_LONG_STREAM=true for 65-second regression")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		end := time.NewTimer(65 * time.Second)
		defer end.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n")
				w.(http.Flusher).Flush()
			case <-end.C:
				io.WriteString(w, "data: {\"type\":\"response.completed\"}\n\n")
				return
			}
		}
	}))
	defer server.Close()
	client := newProxyUpstreamHTTPClient()
	defer client.CloseIdleConnections()
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(body), "response.completed") {
		t.Fatalf("long stream truncated: %v", err)
	}
}
