package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yurika0211/luckyagent-app/bootstrap"
	"github.com/yurika0211/luckyagent-app/channelapp/websocket"
	"github.com/yurika0211/luckyagent-runtime/config"
	"github.com/yurika0211/luckyagent-runtime/session"
)

func TestSessionHistoryAndTraceReportStorageErrors(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	dir := t.TempDir()
	seed := session.NewSession("unreadable-history", dir)
	if err := seed.AddMessageWithError("user", "original durable message"); err != nil {
		t.Fatal(err)
	}
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	mgr, err := session.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	cold, ok := mgr.Get(seed.ID)
	if !ok {
		t.Fatal("missing cold session fixture")
	}
	a.Sessions().Upsert(cold)
	path := filepath.Join(dir, seed.ID, "messages.jsonl")
	backup := path + ".backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "?limit=5", "/tools", "/tools?turn=first"} {
		w := httptest.NewRecorder()
		s.handleSessionByID(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+seed.ID+suffix, nil))
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "load session history failed") {
			t.Fatalf("%s: status=%d body=%s", suffix, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"messages"`) || strings.Contains(w.Body.String(), `"records"`) {
			t.Fatalf("storage error was returned as a successful empty result: %s", w.Body.String())
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleSessionByID(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+seed.ID, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "original durable message") {
		t.Fatalf("repaired history did not recover on retry: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSessionMetadataWriteErrorsDoNotReturnSuccess(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	sess := a.Sessions().Ensure("metadata-write-failure")
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Sessions().Dir(), sess.ID+".meta.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		url := "/api/v1/sessions"
		if method == http.MethodPatch {
			url += "/" + sess.ID
		}
		body := `{"id":"metadata-write-failure","title":"must not acknowledge"}`
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		if method == http.MethodPost {
			s.handleSessions(w, r)
		} else {
			s.handleSessionByID(w, r)
		}
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "save session failed") {
			t.Fatalf("%s: status=%d body=%s", method, w.Code, w.Body.String())
		}
	}
}

func TestDeletedSessionHandleReturnsGoneForMetadataAndTrace(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	sess := a.Sessions().Ensure("deleted-handle")
	if err := a.Sessions().Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.patchSession(w, httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+sess.ID, strings.NewReader(`{"title":"late write"}`)), sess)
	if w.Code != http.StatusGone {
		t.Fatalf("late metadata write status=%d body=%s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleSessionToolTrace(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/tools", nil), sess)
	if w.Code != http.StatusGone {
		t.Fatalf("late trace read status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDeleteSessionCancelsHTTPAndWebSocketWork(t *testing.T) {
	for _, surface := range []string{"sync", "sse", "websocket"} {
		t.Run(surface, func(t *testing.T) {
			started, cancelled := make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			var startOnce, cancelOnce sync.Once
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				startOnce.Do(func() { close(started) })
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					cancelOnce.Do(func() { close(cancelled) })
				case <-release:
				}
			}))
			t.Cleanup(upstream.Close)
			t.Cleanup(func() { close(release) })
			cfg, err := config.NewManagerWithDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{"provider": "openai", "api_key": "test", "api_base": upstream.URL, "model": "gpt-test", "stream_mode": "native"} {
				if err := cfg.Set(key, value); err != nil {
					t.Fatal(err)
				}
			}
			a, err := bootstrap.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.Close() })
			s := New(a, DefaultServerConfig())
			t.Cleanup(func() { _ = s.Stop() })
			sess := a.Sessions().Ensure("delete-in-flight")
			if err := sess.Save(); err != nil {
				t.Fatal(err)
			}
			finished := make(chan struct{})
			response := httptest.NewRecorder()
			if surface == "websocket" {
				client := &websocket.Client{SessionID: sess.ID, Send: make(chan *websocket.Message, 32)}
				data, _ := json.Marshal(websocket.ChatData{Message: "wait until deleted", Stream: true})
				s.wsHandler.HandleMessage(client, &websocket.Message{Type: websocket.TypeChat, ID: "first", Data: data})
				go func() { s.wsHandler.WaitSession(sess.ID, 0); close(finished) }()
			} else {
				go func() {
					defer close(finished)
					r := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(`{"message":"wait until deleted","session_id":"delete-in-flight"}`))
					if surface == "sse" {
						s.handleChat(response, r)
					} else {
						s.handleChatSync(response, r)
					}
				}()
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("request never reached provider")
			}
			if surface == "websocket" {
				data, _ := json.Marshal(websocket.ChatData{Message: "must not start from deleted queue", Stream: true})
				s.wsHandler.HandleMessage(&websocket.Client{SessionID: sess.ID, Send: make(chan *websocket.Message, 32)}, &websocket.Message{Type: websocket.TypeChat, ID: "queued", Data: data})
			}
			deleted := httptest.NewRecorder()
			s.handleSessionByID(deleted, httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/"+sess.ID, nil))
			if deleted.Code != http.StatusOK {
				t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
			}
			select {
			case <-cancelled:
			case <-time.After(3 * time.Second):
				t.Fatal("deleting session did not cancel its provider request")
			}
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("deleted session's handler did not finish")
			}
			if surface == "sse" && strings.Contains(response.Body.String(), `"type":"complete"`) {
				t.Fatalf("deleted request produced successful completion: %s", response.Body.String())
			}
			if surface == "sync" && response.Code == http.StatusOK {
				t.Fatalf("deleted request returned success: %s", response.Body.String())
			}
			if calls.Load() != 1 {
				t.Fatalf("unexpected execution after deletion: provider calls=%d", calls.Load())
			}
			if _, ok := a.Sessions().Get(sess.ID); ok {
				t.Fatal("deleted session reappeared in manager")
			}
			if err := sess.Save(); !errors.Is(err, session.ErrSessionDeleted) {
				t.Fatalf("stale handle save=%v, want ErrSessionDeleted", err)
			}
			if _, err := os.Stat(filepath.Join(a.Sessions().Dir(), sess.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deleted transcript directory reappeared: %v", err)
			}
		})
	}
}

func TestDeleteFailureLeavesRegisteredHTTPWorkRunning(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	sess := a.Sessions().Ensure("delete-failure")
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}
	// A nonempty directory where a metadata file belongs makes Delete fail on
	// every platform, including privileged test environments.
	meta := filepath.Join(a.Sessions().Dir(), sess.ID+".meta.json")
	if err := os.Remove(meta); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(meta, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meta, "block"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := s.registerChatCancellation(context.Background(), sess.ID)
	defer cancel()
	w := httptest.NewRecorder()
	s.handleSessionByID(w, httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/"+sess.ID, nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
	if ctx.Err() != nil {
		t.Fatal("failed deletion cancelled a live request")
	}
}

type sessionHandoffWriter struct {
	*httptest.ResponseRecorder
	once    sync.Once
	handoff func()
}

func (w *sessionHandoffWriter) Header() http.Header {
	w.once.Do(w.handoff)
	return w.ResponseRecorder.Header()
}

func TestSSEHandoffPreservesValidatedSessionPointer(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	original := a.Sessions().Ensure("sse-same-id")
	if err := original.Save(); err != nil {
		t.Fatal(err)
	}
	var replacement *session.Session
	w := &sessionHandoffWriter{ResponseRecorder: httptest.NewRecorder(), handoff: func() {
		// Headers are prepared after the handler has fetched the requested
		// session but before the Agent is invoked. Replace the ID in this gap.
		if err := a.Sessions().Delete(original.ID); err != nil {
			t.Fatal(err)
		}
		replacement = a.Sessions().Ensure(original.ID)
		if err := replacement.AddMessageWithError("user", "replacement history"); err != nil {
			t.Fatal(err)
		}
	}}
	s.handleChat(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(`{"session_id":"sse-same-id","message":"must not enter the replacement"}`)))
	if replacement == nil || !strings.Contains(w.Body.String(), "session was deleted") || strings.Contains(w.Body.String(), `"type":"complete"`) {
		t.Fatalf("handoff did not fail against the original handle: %s", w.Body.String())
	}
	messages, err := replacement.GetMessagesWithError()
	if err != nil || len(messages) != 1 || messages[0].Content != "replacement history" {
		t.Fatalf("replacement modified: messages=%#v err=%v", messages, err)
	}
}
