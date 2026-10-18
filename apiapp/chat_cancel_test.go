package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appboot "github.com/yurika0211/luckyagent-app/bootstrap"
	"github.com/yurika0211/luckyagent-runtime/config"
)

func TestSSECancellationAlwaysHasCancelledTerminal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	cfg, err := config.NewManagerWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"provider": "openai", "api_key": "test", "api_base": upstream.URL, "model": "gpt-test", "stream_mode": "native"} {
		if err := cfg.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	a, err := appboot.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := New(a, DefaultServerConfig())
	defer s.Stop()
	server := httptest.NewServer(s.loggingMiddleware(http.HandlerFunc(s.handleChat)))
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	for i := 0; i < 12; i++ {
		resp, err := client.Post(server.URL, "application/json", strings.NewReader(`{"message":"wait until cancelled"}`))
		if err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(resp.Body)
		var sessionID string
		for sessionID == "" {
			line, err := reader.ReadString('\n')
			if err != nil {
				resp.Body.Close()
				t.Fatal(err)
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
				resp.Body.Close()
				t.Fatal(err)
			}
			sessionID, _ = frame["session_id"].(string)
		}
		if !s.cancelChat(sessionID) {
			resp.Body.Close()
			t.Fatal("no active request to cancel")
		}
		tail, err := io.ReadAll(reader)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(tail), `"type":"cancelled"`) || strings.Contains(string(tail), `"status":"failed"`) || strings.Contains(string(tail), `"type":"complete"`) {
			t.Fatalf("iteration %d terminal=%s", i, tail)
		}
	}
}
