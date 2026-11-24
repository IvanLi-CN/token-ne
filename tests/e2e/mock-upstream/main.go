package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Scenario struct {
	Status         int    `json:"status,omitempty"`
	Stream         bool   `json:"stream,omitempty"`
	DelayMS        int    `json:"delay_ms,omitempty"`
	ErrorEvent     bool   `json:"error_event,omitempty"`
	InterruptAfter int    `json:"interrupt_after,omitempty"`
	RetryAfter     string `json:"retry_after,omitempty"`
}

type RecordedRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Model   string            `json:"model,omitempty"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type mock struct {
	mu        sync.Mutex
	scenarios []Scenario
	requests  []RecordedRequest
	// catalog overrides the model directory GET /v1/models serves. Empty means
	// the default single mock-model entry; tests set it to create stale entries.
	catalog []string
}

func (m *mock) catalogIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.catalog) == 0 {
		return []string{"mock-model"}
	}
	return append([]string(nil), m.catalog...)
}

func (m *mock) nextScenario() Scenario {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.scenarios) == 0 {
		return Scenario{Status: http.StatusOK}
	}
	next := m.scenarios[0]
	m.scenarios = m.scenarios[1:]
	if next.Status == 0 {
		next.Status = http.StatusOK
	}
	return next
}

func (m *mock) record(r *http.Request, body []byte) {
	headers := map[string]string{}
	for _, key := range []string{
		"Authorization", "Content-Type", "X-Request-Id",
		"X-Token-Ne-Request-Id", "X-Token-Ne-Attempt", "X-Token-Ne-No-Status-Rewrite",
	} {
		if value := r.Header.Get(key); value != "" {
			headers[strings.ToLower(key)] = value
		}
	}
	var envelope struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &envelope)
	m.mu.Lock()
	m.requests = append(m.requests, RecordedRequest{
		Method: r.Method, Path: r.URL.Path, Model: envelope.Model, Headers: headers, Body: string(body),
	})
	if len(m.requests) > 200 {
		m.requests = m.requests[len(m.requests)-200:]
	}
	m.mu.Unlock()
}

func (m *mock) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /__mock/scenarios", m.setScenarios)
	mux.HandleFunc("POST /__mock/reset", m.reset)
	mux.HandleFunc("GET /__mock/requests", m.listRequests)
	mux.HandleFunc("POST /__mock/models", m.setCatalog)
	mux.HandleFunc("GET /v1/models", m.models)
	mux.HandleFunc("POST /v1/chat/completions", m.chat)
	mux.HandleFunc("POST /v1/responses", m.responses)
	mux.HandleFunc("POST /v1/embeddings", m.embeddings)
	return mux
}

func (m *mock) setScenarios(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Scenarios []Scenario `json:"scenarios"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid scenario payload"})
		return
	}
	m.mu.Lock()
	m.scenarios = append([]Scenario(nil), payload.Scenarios...)
	m.mu.Unlock()
	writeJSON(w, 200, map[string]any{"status": "ok", "queued": len(payload.Scenarios)})
}

func (m *mock) reset(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	m.scenarios = nil
	m.requests = nil
	m.catalog = nil
	m.mu.Unlock()
	writeJSON(w, 200, map[string]any{"status": "ok"})
}

func (m *mock) setCatalog(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid catalog payload"})
		return
	}
	m.mu.Lock()
	m.catalog = append([]string(nil), payload.Models...)
	m.mu.Unlock()
	writeJSON(w, 200, map[string]any{"status": "ok", "models": payload.Models})
}

func (m *mock) listRequests(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	requests := append([]RecordedRequest(nil), m.requests...)
	m.mu.Unlock()
	writeJSON(w, 200, map[string]any{"requests": requests})
}

func (m *mock) models(w http.ResponseWriter, _ *http.Request) {
	data := []map[string]any{}
	for _, id := range m.catalogIDs() {
		data = append(data, map[string]any{"id": id, "object": "model", "created": 1700000000, "owned_by": "mock"})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func (m *mock) chat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeJSON(w, 400, errorBody("invalid request"))
		return
	}
	m.record(r, body)
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	s := m.nextScenario()
	if s.DelayMS > 0 {
		time.Sleep(time.Duration(s.DelayMS) * time.Millisecond)
	}
	if s.Status != http.StatusOK {
		if s.RetryAfter != "" {
			w.Header().Set("Retry-After", s.RetryAfter)
		}
		writeJSON(w, s.Status, errorBody("mock upstream status"))
		return
	}
	if s.Stream || req.Stream {
		m.chatStream(w, req.Model, s)
		return
	}
	writeJSON(w, 200, map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   req.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": "mock response"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19,
			"prompt_tokens_details":     map[string]any{"cached_tokens": 3},
			"completion_tokens_details": map[string]any{"reasoning_tokens": 2},
		},
	})
}

func (m *mock) chatStream(w http.ResponseWriter, model string, s Scenario) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)
	if s.ErrorEvent {
		fmt.Fprint(w, "event: error\ndata: {\"error\":{\"message\":\"mock stream error\",\"type\":\"server_error\",\"code\":\"overloaded\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	writeSSE(w, `{"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"`+model+`","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
	writeSSE(w, `{"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"`+model+`","choices":[{"index":0,"delta":{"content":"mock "}}]}`)
	if s.InterruptAfter > 0 {
		return
	}
	writeSSE(w, `{"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"`+model+`","choices":[{"index":0,"delta":{"content":"response"},"finish_reason":"stop"}]}`)
	writeSSE(w, `{"id":"chatcmpl-mock","object":"chat.completion.chunk","model":"`+model+`","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}}`)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (m *mock) responses(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	m.record(r, body)
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	s := m.nextScenario()
	if s.DelayMS > 0 {
		time.Sleep(time.Duration(s.DelayMS) * time.Millisecond)
	}
	if s.Status != http.StatusOK {
		if s.RetryAfter != "" {
			w.Header().Set("Retry-After", s.RetryAfter)
		}
		writeJSON(w, s.Status, errorBody("mock upstream status"))
		return
	}
	if s.Stream || req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		if s.ErrorEvent {
			fmt.Fprint(w, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n")
			return
		}
		writeSSE(w, `{"type":"response.created","response":{"id":"resp_mock","model":"`+req.Model+`","status":"in_progress"}}`)
		writeSSE(w, `{"type":"response.output_text.delta","response_id":"resp_mock","delta":"mock "}`)
		if s.InterruptAfter > 0 {
			return
		}
		writeSSE(w, `{"type":"response.output_text.delta","response_id":"resp_mock","delta":"response"}`)
		writeSSE(w, `{"type":"response.completed","response":{"id":"resp_mock","model":"`+req.Model+`","status":"completed","usage":{"input_tokens":12,"output_tokens":7,"total_tokens":19,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}`)
		return
	}
	writeJSON(w, 200, map[string]any{
		"id": "resp_mock", "object": "response", "model": req.Model, "status": "completed",
		"output": []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "mock response"}}}},
		"usage":  map[string]any{"input_tokens": 12, "output_tokens": 7, "total_tokens": 19},
	})
}

func (m *mock) embeddings(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	m.record(r, body)
	writeJSON(w, 200, map[string]any{
		"object": "list", "model": "mock-embedding",
		"data":  []map[string]any{{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}}},
		"usage": map[string]any{"prompt_tokens": 4, "total_tokens": 4},
	})
}

func writeSSE(w http.ResponseWriter, payload string) {
	fmt.Fprintf(w, "data: %s\n\n", payload)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func errorBody(message string) map[string]any {
	return map[string]any{"error": map[string]any{"message": message, "type": "mock_error", "code": "mock_status", "param": nil}}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func main() {
	listen := os.Getenv("MOCK_UPSTREAM_LISTEN")
	if listen == "" {
		listen = "0.0.0.0:8080"
	}
	m := &mock{}
	slog.Info("mock upstream listening", "address", listen)
	if err := http.ListenAndServe(listen, m.routes()); err != nil {
		slog.Error("mock upstream failed", "error", err)
		os.Exit(1)
	}
}
