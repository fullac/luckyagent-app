package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yurika0211/lucky-agent/agent"
	"github.com/yurika0211/luckyagent-runtime/session"
	"github.com/yurika0211/luckyagent-runtime/tool"
)

func TestChatRetryDoesNotExecuteAgainAndConflictsAreRejected(t *testing.T) {
	mgr, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	runtime := &stubAgentRuntime{sessions: mgr, tools: tool.NewRegistry(), chatInputFn: func(ctx context.Context, id string, input agent.UserTurnInput) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "one result", nil
	}}
	h := NewAgentHandler(runtime)
	client := &Client{SessionID: "idempotent", Send: make(chan *Message, 256)}
	send := func(text string) {
		h.HandleMessage(client, &Message{Type: TypeChat, ID: "same-request", Data: mustChatData(ChatData{Message: text})})
	}
	send("execute once")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not start")
	}
	for i := 0; i < 10; i++ {
		send("execute once")
	}
	send("different payload")
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	close(release)
	if !h.WaitSession(client.SessionID, 2*time.Second) {
		t.Fatal("run did not finish")
	}
	send("execute once")
	if calls.Load() != 1 {
		t.Fatal("completed request executed again")
	}
	var conflict, result bool
	for len(client.Send) > 0 {
		msg := <-client.Send
		if msg.Type == TypeStreamEnd {
			result = true
		}
		if msg.Type == TypeError {
			var data ErrorData
			_ = msg.ParseData(&data)
			if data.Code == "REQUEST_CONFLICT" {
				conflict = true
			}
		}
	}
	if !conflict || !result {
		t.Fatalf("conflict=%t result=%t", conflict, result)
	}
}

func TestReconnectDeliversMoreThanClientBuffer(t *testing.T) {
	h := NewAgentHandler(nil)
	now := time.Now().UTC()
	if err := h.store.UpsertRun(persistedRun{ID: "run", SessionID: "replay", State: "completed", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cursor, _ := NewMessage(TypeStatus, "replay", StatusData{State: "thinking"})
	if err := h.store.AppendEvent("replay", "run", cursor); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		kind := TypeStreamChunk
		if i == 999 {
			kind = TypeStreamEnd
		}
		event, _ := NewMessage(kind, "replay", map[string]any{"content": fmt.Sprint(i)})
		if err := h.store.AppendEvent("replay", "run", event); err != nil {
			t.Fatal(err)
		}
	}
	client := &Client{SessionID: "replay", Send: make(chan *Message, 8)}
	done := make(chan struct{})
	go func() { defer close(done); h.HandleReconnect(client, &Message{Data: mustReconnectData(cursor.ID)}) }()
	count, ended := 0, false
	timeout := time.After(3 * time.Second)
	for {
		select {
		case msg := <-client.Send:
			if msg == nil {
				t.Fatal("client closed before replay completed")
			}
			if msg.Type == TypeStreamEnd {
				ended = true
			}
			if msg.RunID == "run" {
				count++
			}
			if msg.Type == TypeStatus {
				var data StatusData
				_ = msg.ParseData(&data)
				if data.State == "connected" {
					<-done
					if count != 1000 || !ended {
						t.Fatalf("count=%d ended=%t", count, ended)
					}
					return
				}
			}
		case <-timeout:
			t.Fatal("replay stalled")
		}
	}
}

func mustReconnectData(cursor string) json.RawMessage {
	raw, _ := json.Marshal(ReconnectData{LastMessageID: cursor})
	return raw
}

func TestChatDoesNotExecuteWhenPersistenceFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	h := NewAgentHandler(&stubAgentRuntime{tools: tool.NewRegistry(), chatInputFn: func(context.Context, string, agent.UserTurnInput) (string, error) { calls.Add(1); return "", nil }})
	h.store = newRunStore(root)
	client := &Client{SessionID: "persist", Send: make(chan *Message, 8)}
	h.HandleMessage(client, &Message{Type: TypeChat, ID: "request", Data: mustChatData(ChatData{Message: "write"})})
	if calls.Load() != 0 {
		t.Fatal("executed a request that was not durably accepted")
	}
	msg := <-client.Send
	var data ErrorData
	_ = msg.ParseData(&data)
	if data.Code != "PERSIST_FAILED" {
		t.Fatalf("error=%#v", data)
	}
}

func TestRetryReplayDoesNotRegressCompletedRunToRunning(t *testing.T) {
	h := NewAgentHandler(nil)
	now := time.Now().UTC()
	stale := persistedRun{ID: "run", SessionID: "session", State: "running", CreatedAt: now, UpdatedAt: now}
	current := stale
	current.State = "completed"
	if err := h.store.UpsertRun(current); err != nil {
		t.Fatal(err)
	}
	end, _ := NewMessage(TypeStreamEnd, current.SessionID, StreamEndData{FullResponse: "done"})
	if err := h.store.AppendEvent(current.SessionID, current.ID, end); err != nil {
		t.Fatal(err)
	}
	client := &Client{SessionID: current.SessionID, Send: make(chan *Message, 8)}
	h.replayAcceptedRun(client, stale)
	for len(client.Send) > 0 {
		msg := <-client.Send
		if msg.Type == TypeStatus {
			var data StatusData
			_ = msg.ParseData(&data)
			if data.State == "running" {
				t.Fatal("completed result followed by stale running state")
			}
		}
	}
}

func TestStreamWithoutDoneOrErrorCauseFailsTheRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []agent.ChatEvent
	}{
		{"partial EOF", []agent.ChatEvent{{Type: agent.ChatEventContent, Content: "partial"}}},
		{"empty EOF", nil},
		{"nil error", []agent.ChatEvent{{Type: agent.ChatEventError}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, err := session.NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			h := NewAgentHandler(&stubAgentRuntime{sessions: mgr, tools: tool.NewRegistry(), chatStreamInputFn: func(context.Context, string, agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
				out := make(chan agent.ChatEvent, len(tc.events))
				for _, event := range tc.events {
					out <- event
				}
				close(out)
				return out, nil
			}})
			client := &Client{SessionID: "incomplete", Send: make(chan *Message, 32)}
			h.HandleMessage(client, &Message{Type: TypeChat, ID: "request", Data: mustChatData(ChatData{Message: "test", Stream: true})})
			if !h.WaitSession(client.SessionID, 2*time.Second) {
				t.Fatal("run did not finish")
			}
			deadline := time.Now().Add(time.Second)
			for {
				run, ok, err := h.store.GetRun(client.SessionID, "request")
				if err != nil {
					t.Fatal(err)
				}
				if ok && run.State == "error" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("run state=%#v", run)
				}
				time.Sleep(time.Millisecond)
			}
			for len(client.Send) > 0 {
				if msg := <-client.Send; msg.Type == TypeStreamEnd {
					t.Fatal("incomplete stream emitted success")
				}
			}
		})
	}
}
