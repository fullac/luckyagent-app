package websocket

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yurika0211/lucky-agent/agent"
	"github.com/yurika0211/luckyagent-runtime/session"
	"github.com/yurika0211/luckyagent-runtime/tool"
)

func TestSyncChatReportsUnreadableOrDeletedSessionHistory(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		name := "unreadable"
		if deleted {
			name = "deleted"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			seed := session.NewSession("storage-history", dir)
			if err := seed.AddMessageWithError("user", "durable history"); err != nil {
				t.Fatal(err)
			}
			if err := seed.Save(); err != nil {
				t.Fatal(err)
			}
			mgr, err := session.NewManager(dir)
			if err != nil {
				t.Fatal(err)
			}
			runtime := &stubAgentRuntime{sessions: mgr, tools: tool.NewRegistry(), chatInputFn: func(context.Context, string, agent.UserTurnInput) (string, error) {
				if deleted {
					if err := mgr.Delete(seed.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					path := filepath.Join(dir, seed.ID, "messages.jsonl")
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				return "must not be reported as success", nil
			}}
			h := NewAgentHandler(runtime)
			client := &Client{SessionID: seed.ID, Send: make(chan *Message, 8)}
			err = h.syncChatRun(context.Background(), client, ChatData{Message: "test"}, "request", "")
			if err == nil {
				t.Fatal("unavailable history was accepted as an empty successful session")
			}
			if deleted && !errors.Is(err, session.ErrSessionDeleted) {
				t.Fatalf("deleted session error=%v", err)
			}
			wantCode := "SESSION_HISTORY_FAILED"
			if deleted {
				wantCode = "SESSION_DELETED"
			}
			found := false
			for len(client.Send) > 0 {
				msg := <-client.Send
				if msg.Type == TypeStreamEnd {
					t.Fatal("storage failure emitted a successful stream_end")
				}
				if msg.Type == TypeError {
					var data ErrorData
					if err := msg.ParseData(&data); err != nil || data.Code != wantCode {
						t.Fatalf("error data=%#v err=%v", data, err)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("session history storage failure was not sent to client")
			}
		})
	}
}

type sessionHandoffRuntime struct {
	*stubAgentRuntime
	armed      atomic.Bool
	replace    func()
	byIDCalls  atomic.Int32
	executions atomic.Int32
}

func (r *sessionHandoffRuntime) Sessions() *session.Manager {
	if r.armed.Load() {
		r.replace()
	}
	return r.stubAgentRuntime.sessions
}

func (r *sessionHandoffRuntime) RunLoopWithSessionInput(_ context.Context, sess *session.Session, _ agent.UserTurnInput, _ agent.LoopConfig) (*agent.LoopResult, error) {
	r.replace()
	if _, err := sess.GetMessagesWithError(); err != nil {
		return nil, err
	}
	r.executions.Add(1)
	return &agent.LoopResult{Response: "unexpected execution"}, nil
}

func (r *sessionHandoffRuntime) ChatWithSessionStreamInputWithLoopConfig(ctx context.Context, id string, input agent.UserTurnInput, cfg agent.LoopConfig) (<-chan agent.ChatEvent, error) {
	r.byIDCalls.Add(1)
	r.replace()
	sess, _ := r.stubAgentRuntime.sessions.Get(id)
	return r.ChatWithSessionStreamForSession(ctx, sess, input, cfg)
}

func (r *sessionHandoffRuntime) ChatWithSessionStreamForSession(_ context.Context, sess *session.Session, _ agent.UserTurnInput, _ agent.LoopConfig) (<-chan agent.ChatEvent, error) {
	r.replace()
	if _, err := sess.GetMessagesWithError(); err != nil {
		return nil, err
	}
	r.executions.Add(1)
	out := make(chan agent.ChatEvent, 1)
	out <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "unexpected execution"}
	close(out)
	return out, nil
}

func TestWebSocketExecutionHandoffPreservesOriginalSessionPointer(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "sync"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			mgr, err := session.NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			original := mgr.Ensure("same-id")
			if err := original.Save(); err != nil {
				t.Fatal(err)
			}
			var replacement *session.Session
			var replaceOnce sync.Once
			runtime := &sessionHandoffRuntime{stubAgentRuntime: &stubAgentRuntime{sessions: mgr, tools: tool.NewRegistry()}}
			runtime.replace = func() {
				replaceOnce.Do(func() {
					if err := mgr.Delete(original.ID); err != nil {
						t.Fatal(err)
					}
					replacement = mgr.Ensure(original.ID)
					if err := replacement.AddMessageWithError("user", "replacement history"); err != nil {
						t.Fatal(err)
					}
				})
			}
			h := NewAgentHandler(runtime)
			h.runners[original.ID] = &sessionRunner{active: &queuedRun{id: "run", session: original}}
			bound, err := h.sessionForRun(context.Background(), original.ID, "run")
			if err != nil || bound != original {
				t.Fatalf("initial binding=%p error=%v", bound, err)
			}
			// The hook replaces the ID at the execution boundary, after validation.
			// Any second lookup by ID would now obtain the replacement handle.
			runtime.armed.Store(true)
			if streaming {
				client := &Client{SessionID: original.ID, Send: make(chan *Message, 8)}
				err = h.streamChatRun(context.Background(), client, ChatData{Message: "test", Stream: true}, "run", "run")
				for len(client.Send) > 0 {
					if msg := <-client.Send; msg.Type == TypeStreamEnd {
						t.Fatal("handoff emitted successful completion against a replacement session")
					}
				}
			} else {
				_, _, err = h.syncTurn(context.Background(), bound, original.ID, agent.TextUserTurnInput("test"), ChatData{})
			}
			if !errors.Is(err, session.ErrSessionDeleted) {
				t.Fatalf("handoff error=%v, want ErrSessionDeleted", err)
			}
			if runtime.byIDCalls.Load() != 0 || runtime.executions.Load() != 0 {
				t.Fatalf("ID lookups=%d executions=%d", runtime.byIDCalls.Load(), runtime.executions.Load())
			}
			messages, err := replacement.GetMessagesWithError()
			if err != nil || len(messages) != 1 || messages[0].Content != "replacement history" {
				t.Fatalf("replacement modified: messages=%#v err=%v", messages, err)
			}
		})
	}
}

func TestQueuedTurnsRemainBoundToDeletedSessionHandle(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		name := "deleted"
		if recreate {
			name = "same ID recreated"
		}
		t.Run(name, func(t *testing.T) {
			mgr, err := session.NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			runtime := &stubAgentRuntime{sessions: mgr, tools: tool.NewRegistry(), chatInputFn: func(context.Context, string, agent.UserTurnInput) (string, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-release
				}
				return "old execution must not belong to a new session", nil
			}}
			h := NewAgentHandler(runtime)
			client := &Client{SessionID: "initially-unknown", Send: make(chan *Message, 32)}
			send := func(id string) {
				h.HandleMessage(client, &Message{Type: TypeChat, ID: id, Data: mustChatData(ChatData{Message: id})})
			}
			send("active")
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("first turn did not start")
			}
			send("queued")
			original, ok := mgr.Get(client.SessionID)
			if !ok {
				t.Fatal("unknown session was not bound during acceptance")
			}
			if err := mgr.Delete(client.SessionID); err != nil {
				t.Fatal(err)
			}
			var replacement *session.Session
			if recreate {
				replacement = mgr.Ensure(client.SessionID)
				if replacement == original {
					t.Fatal("fixture did not create a new session handle")
				}
				if err := replacement.AddMessageWithError("user", "new session's history"); err != nil {
					t.Fatal(err)
				}
			}
			// Deliberately let the active turn finish naturally before an explicit
			// CancelSession can clear the queue. Tombstones must still block both
			// its late success and the queued turn from creating/adopting a handle.
			close(release)
			if !h.WaitSession(client.SessionID, 2*time.Second) {
				t.Fatal("deleted queue did not finish")
			}
			if calls.Load() != 1 {
				t.Fatalf("queued turn executed after deletion: calls=%d", calls.Load())
			}
			deletedErrors := 0
			for len(client.Send) > 0 {
				msg := <-client.Send
				if msg.Type == TypeStreamEnd {
					t.Fatal("deleted session emitted a successful end")
				}
				if msg.Type == TypeError {
					var data ErrorData
					if err := msg.ParseData(&data); err != nil || data.Code != "SESSION_DELETED" {
						t.Fatalf("error=%#v err=%v", data, err)
					}
					deletedErrors++
				}
			}
			if deletedErrors != 2 {
				t.Fatalf("deleted errors=%d, want active and queued failures", deletedErrors)
			}
			current, exists := mgr.Get(client.SessionID)
			if !recreate && exists {
				t.Fatal("queued work recreated the deleted session")
			}
			if recreate {
				if !exists || current != replacement {
					t.Fatal("queued work replaced the new session")
				}
				messages, err := replacement.GetMessagesWithError()
				if err != nil || len(messages) != 1 || messages[0].Content != "new session's history" {
					t.Fatalf("old work modified replacement history: messages=%#v err=%v", messages, err)
				}
			}
		})
	}
}
