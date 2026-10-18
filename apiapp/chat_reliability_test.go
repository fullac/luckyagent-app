package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatRejectsUnknownSessionBeforeExecuting(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	for _, handler := range []http.HandlerFunc{s.handleChatSync, s.handleChat} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(`{"message":"hello","session_id":"does-not-exist"}`)))
		if w.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
			t.Fatal("committed SSE before session validation")
		}
	}
	if _, ok := a.Sessions().Get("does-not-exist"); ok {
		t.Fatal("invalid session was silently created")
	}
}

func TestChatJSONLimitsAndTrailingInput(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"message":"/help","padding":"` + strings.Repeat("x", 3<<20) + `"}`, http.StatusRequestEntityTooLarge},
		{`{"message":"/help"} {"message":"ignored"}`, http.StatusBadRequest},
	} {
		for _, handler := range []http.HandlerFunc{s.handleChatSync, s.handleChat} {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(tc.body)))
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
		}
	}
}

func TestSSEFailureRetainsMessageAndHasNoSuccessCompletion(t *testing.T) {
	s := longChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"EXPECTED_PROVIDER_FAILURE","type":"invalid_request_error"}}`)
	})
	resp, err := http.Post(s.URL, "application/json", strings.NewReader(`{"message":"test failure"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var errorFrame map[string]any
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		if frame["type"] == "complete" {
			t.Fatalf("failure followed by success: %s", body)
		}
		if frame["type"] == "error" {
			errorFrame = frame
		}
	}
	message, ok := errorFrame["error"].(string)
	if !ok || !strings.Contains(message, "EXPECTED_PROVIDER_FAILURE") {
		t.Fatalf("lost error message: %s", body)
	}
	if errorFrame["status"] != "failed" || errorFrame["run_id"] == "" || errorFrame["event_id"] == "" {
		t.Fatalf("missing failure identity: %s", body)
	}
	if !strings.Contains(string(body), "id: http-") {
		t.Fatalf("missing SSE event id: %s", body)
	}
}
