package telegram

import (
	"github.com/yurika0211/luckyagent-gateway/telegram/presentation"
	"testing"
)

// TestHandlerSetDataDir 测试 SetDataDir
func TestHandlerSetDataDir(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "test-token"
	adapter := NewAdapter(cfg)
	handler := NewHandler(adapter, nil)

	tmpDir := t.TempDir()
	handler.SetDataDir(tmpDir)

	h := handler
	if h.dataDir != tmpDir {
		t.Errorf("expected dataDir %s, got %s", tmpDir, h.dataDir)
	}
}

// TestHandlerSetSessionID 测试 setSessionID
func TestHandlerSetSessionID(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "test-token"
	adapter := NewAdapter(cfg)
	handler := NewHandler(adapter, nil)

	h := handler
	h.setSessionID("chat-1", "test-session")
	if h.sessions["chat-1"] != "test-session" {
		t.Errorf("expected test-session, got %s", h.sessions["chat-1"])
	}
}

// TestHandlerHasSession 测试 hasSession
func TestHandlerHasSession(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "test-token"
	adapter := NewAdapter(cfg)
	handler := NewHandler(adapter, nil)

	h := handler
	h.sessions["chat-1"] = "session-1"

	if !h.hasSession("chat-1") {
		t.Error("expected session to exist")
	}
	if h.hasSession("chat-2") {
		t.Error("expected session not to exist")
	}
}

// TestHandlerTruncateString 测试 presentation.TruncateString
func TestHandlerTruncateString(t *testing.T) {
	result := presentation.TruncateString("hello", 10)
	if result != "hello" {
		t.Errorf("expected 'hello', got '%s'", result)
	}

	result = presentation.TruncateString("this is a long string", 10)
	if len(result) != 10 {
		t.Errorf("expected length 10, got %d", len(result))
	}
}

// TestHandlerConcurrentSessions 测试并发 session 操作
func TestHandlerConcurrentSessions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "test-token"
	adapter := NewAdapter(cfg)
	handler := NewHandler(adapter, nil)

	h := handler
	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func(idx int) {
			chatID := "chat-" + string(rune('0'+idx))
			h.setSessionID(chatID, "session-"+string(rune('0'+idx)))
			h.hasSession(chatID)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
