package sdk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yurika0211/luckyagent-runtime/session"
)

func TestGetSessionPreservesHistoryStorageErrors(t *testing.T) {
	a, err := New(Config{HomeDir: t.TempDir(), Provider: "openai", Model: "gpt-test", APIKey: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	dir := t.TempDir()
	seed := session.NewSession("unreadable-sdk-history", dir)
	if err := seed.AddMessageWithError("user", "original message"); err != nil {
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
	a.inner.Sessions().Upsert(cold)
	path := filepath.Join(dir, seed.ID, "messages.jsonl")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := a.GetSession(seed.ID); err == nil || got != nil || !strings.Contains(err.Error(), "sdk: load session history") {
		t.Fatalf("session=%#v error=%v", got, err)
	}
}

func TestDeleteSessionCancelsEmbeddedAgentTurn(t *testing.T) {
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var startOnce, cancelOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	a, err := New(Config{HomeDir: t.TempDir(), Provider: "openai", Model: "gpt-test", APIKey: "test", APIBase: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	t.Cleanup(func() { close(release) })
	id, err := a.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := a.inner.Sessions().Get(id)
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.ChatSession(ctx, id, "wait for deletion"); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("embedded request did not reach provider")
	}
	if err := a.DeleteSession(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("deleting SDK session did not cancel its provider request")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("deleted SDK session returned a successful answer")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("embedded Agent turn did not stop")
	}
	if err := sess.Save(); !errors.Is(err, session.ErrSessionDeleted) {
		t.Fatalf("stale session save=%v", err)
	}
	if _, ok := a.inner.Sessions().Get(id); ok {
		t.Fatal("deleted SDK session reappeared")
	}
}
