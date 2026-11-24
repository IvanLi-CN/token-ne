package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*mock, *httptest.Server) {
	t.Helper()
	m := &mock{}
	server := httptest.NewServer(m.routes())
	t.Cleanup(server.Close)
	return m, server
}

func postJSON(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	response, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}

func TestChatNonStreamAndRequestRecord(t *testing.T) {
	_, server := newTestServer(t)
	response, body := postJSON(t, server.URL+"/v1/chat/completions", `{"model":"upstream-model","messages":[]}`)
	if response.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	var payload struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Usage.TotalTokens != 19 {
		t.Fatalf("total_tokens = %d", payload.Usage.TotalTokens)
	}
	_, recorded := postJSON(t, server.URL+"/__mock/reset", `{}`)
	if !strings.Contains(string(recorded), `"status":"ok"`) {
		t.Fatalf("reset response = %s", recorded)
	}
}

func TestScenarioRetryAndStreamError(t *testing.T) {
	m, server := newTestServer(t)
	m.mu.Lock()
	m.scenarios = []Scenario{
		{Status: 503, RetryAfter: "1"},
		{Stream: true, ErrorEvent: true},
	}
	m.mu.Unlock()

	response, _ := postJSON(t, server.URL+"/v1/chat/completions", `{"model":"upstream-model","stream":true}`)
	if response.StatusCode != 503 || response.Header.Get("Retry-After") != "1" {
		t.Fatalf("first status = %d retry-after = %q", response.StatusCode, response.Header.Get("Retry-After"))
	}
	response, body := postJSON(t, server.URL+"/v1/chat/completions", `{"model":"upstream-model","stream":true}`)
	if response.StatusCode != 200 {
		t.Fatalf("second status = %d", response.StatusCode)
	}
	if !strings.Contains(string(body), "event: error") {
		t.Fatalf("stream error missing: %s", body)
	}
}

func TestRequestCapture(t *testing.T) {
	_, server := newTestServer(t)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"mapped-model"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test")
	request.Header.Set("X-Token-Ne-Attempt", "2")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	requestsResponse, err := http.Get(server.URL + "/__mock/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer requestsResponse.Body.Close()
	var payload struct {
		Requests []RecordedRequest `json:"requests"`
	}
	if err := json.NewDecoder(requestsResponse.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Requests) != 1 {
		t.Fatalf("requests = %d", len(payload.Requests))
	}
	if payload.Requests[0].Model != "mapped-model" || payload.Requests[0].Headers["x-token-ne-attempt"] != "2" {
		t.Fatalf("unexpected request: %+v", payload.Requests[0])
	}
}

func TestCatalogOverrideAndReset(t *testing.T) {
	m, server := newTestServer(t)
	response, body := postJSON(t, server.URL+"/__mock/models", `{"models":["mock-model","mock-retired"]}`)
	if response.StatusCode != 200 {
		t.Fatalf("set catalog: %d %s", response.StatusCode, body)
	}
	if ids := m.catalogIDs(); len(ids) != 2 || ids[1] != "mock-retired" {
		t.Fatalf("catalog = %v", ids)
	}
	res, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(raw), "mock-retired") || !strings.Contains(string(raw), "mock-model") {
		t.Fatalf("models = %s", raw)
	}
	if _, body = postJSON(t, server.URL+"/__mock/reset", `{}`); !strings.Contains(string(body), "ok") {
		t.Fatalf("reset = %s", body)
	}
	if ids := m.catalogIDs(); len(ids) != 1 || ids[0] != "mock-model" {
		t.Fatalf("reset catalog = %v", ids)
	}
}
