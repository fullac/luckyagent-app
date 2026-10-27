package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yurika0211/lucky-agent/agent"
	appboot "github.com/yurika0211/luckyagent-app/bootstrap"
	"github.com/yurika0211/luckyagent-runtime/config"
	"github.com/yurika0211/luckyagent-runtime/session"
)

func TestChatCompleteReturnsTextWithoutTouchingSessionsOrTools(t *testing.T) {
	var sawTools bool
	var calls int
	a := agentWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"tools"`) {
			sawTools = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"feat(android): 给冷启动补上文案"}}]}`)
	})
	existing := a.Sessions().NewWithTitle("Android session")
	before := sessionSnapshot(a.Sessions().ListInfo())

	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	w := httptest.NewRecorder()
	s.handleChatComplete(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat/complete", strings.NewReader(`{"message":"name this turn"}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 1 || resp["response"] != "feat(android): 给冷启动补上文案" {
		t.Fatalf("response=%s", w.Body.String())
	}
	if calls != 1 || sawTools {
		t.Fatalf("calls=%d tools=%v", calls, sawTools)
	}
	after := sessionSnapshot(a.Sessions().ListInfo())
	if after != before {
		t.Fatalf("sessions changed\nbefore=%s\nafter=%s", before, after)
	}
	got, ok := a.Sessions().Get(existing.ID)
	if !ok || got.MessageCount() != 0 || got.Title != "Android session" {
		t.Fatalf("named session changed: ok=%v", ok)
	}
}

func TestChatCompleteExpandSendsFixedInstructionAndDraft(t *testing.T) {
	var sawTools bool
	var body string
	a := agentWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		if strings.Contains(body, `"tools"`) {
			sawTools = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"扩写后的请求"}}]}`)
	})
	existing := a.Sessions().NewWithTitle("Android session")
	before := sessionSnapshot(a.Sessions().ListInfo())

	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	w := httptest.NewRecorder()
	s.handleChatComplete(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat/complete", strings.NewReader(`{"intent":"expand","message":"修一下启动"}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp) != 1 || resp["response"] != "扩写后的请求" {
		t.Fatalf("response=%s", w.Body.String())
	}
	if sawTools || !strings.Contains(body, "大约 100 字") || !strings.Contains(body, "修一下启动") {
		t.Fatalf("tools=%v body=%s", sawTools, body)
	}
	if sessionSnapshot(a.Sessions().ListInfo()) != before {
		t.Fatal("expand changed sessions")
	}
	got, ok := a.Sessions().Get(existing.ID)
	if !ok || got.MessageCount() != 0 || got.Title != "Android session" {
		t.Fatalf("named session changed: ok=%v", ok)
	}
}

func TestChatCompleteRejectsUnknownIntentBeforeCallingTheModel(t *testing.T) {
	var calls int
	a := agentWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "should not be called", http.StatusInternalServerError)
	})
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	w := httptest.NewRecorder()
	s.handleChatComplete(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat/complete", strings.NewReader(`{"intent":"rewrite","message":"修一下启动"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("upstream calls=%d", calls)
	}
}

func TestChatCompleteRejectsEmptyMessageBeforeCallingTheModel(t *testing.T) {
	var calls int
	a := agentWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "should not be called", http.StatusInternalServerError)
	})
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	w := httptest.NewRecorder()
	s.handleChatComplete(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat/complete", strings.NewReader(`{"message":"  "}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("upstream calls=%d", calls)
	}
}

func TestChatCompleteLeavesSessionsUntouchedWhenTheModelFails(t *testing.T) {
	a := agentWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"upstream down"}}`, http.StatusBadGateway)
	})
	existing := a.Sessions().NewWithTitle("Android session")
	before := sessionSnapshot(a.Sessions().ListInfo())
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	w := httptest.NewRecorder()
	s.handleChatComplete(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat/complete", strings.NewReader(`{"message":"name this turn"}`)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if sessionSnapshot(a.Sessions().ListInfo()) != before {
		t.Fatal("failed completion changed sessions")
	}
	got, ok := a.Sessions().Get(existing.ID)
	if !ok || got.MessageCount() != 0 {
		t.Fatalf("named session changed: ok=%v", ok)
	}
}

func agentWithUpstream(t *testing.T, upstream http.HandlerFunc) *agent.Agent {
	t.Helper()
	provider := httptest.NewServer(upstream)
	t.Cleanup(provider.Close)
	cfg, err := config.NewManagerWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"provider": "openai",
		"api_key":  "test",
		"api_base": provider.URL,
		"model":    "gpt-test",
	} {
		if err := cfg.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	a, err := appboot.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func sessionSnapshot(infos []session.SessionInfo) string {
	var b strings.Builder
	for _, info := range infos {
		b.WriteString(info.ID)
		b.WriteByte('|')
		b.WriteString(info.Title)
		b.WriteByte('|')
		b.WriteString(itoa(info.MessageCount))
		b.WriteByte('\n')
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
