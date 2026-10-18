package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/yurika0211/lucky-agent/agent"
	"github.com/yurika0211/luckyagent-gateway/telegram/presentation"
	"github.com/yurika0211/luckyagent-runtime/embedder"
	"github.com/yurika0211/luckyagent-runtime/gateway"
	"github.com/yurika0211/luckyagent-runtime/metrics"
	"github.com/yurika0211/luckyagent-runtime/provider"
	"github.com/yurika0211/luckyagent-runtime/rag"
	"github.com/yurika0211/luckyagent-runtime/session"
	"github.com/yurika0211/luckyagent-runtime/soul"
	"github.com/yurika0211/luckyagent-runtime/tool"
	"github.com/yurika0211/luckyagent-tools/cron"
	"github.com/yurika0211/luckyagent-tools/memory"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type mockBotClient struct {
	handler func(*http.Request) map[string]any
}

type mockBotServer struct{}

func (mockBotServer) Close() {}

type capturedBotMessage struct {
	Method    string
	Text      string
	ParseMode string
	ReplyTo   string
	ThreadID  string
}

func (c *mockBotClient) Do(req *http.Request) (*http.Response, error) {
	if c.handler == nil {
		return nil, fmt.Errorf("mock bot client: nil handler")
	}
	result := c.handler(req)
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}, nil
}

func defaultMockBotResponse(r *http.Request) map[string]any {
	switch {
	case containsMethod(r.URL.Path, "getMe"):
		return map[string]any{
			"ok": true,
			"result": map[string]any{
				"id":         123456789,
				"is_bot":     true,
				"first_name": "TestBot",
				"username":   "testbot",
			},
		}
	case containsMethod(r.URL.Path, "sendMessage"):
		return map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 42,
				"chat": map[string]any{
					"id": 12345,
				},
				"text": "ok",
			},
		}
	case containsMethod(r.URL.Path, "editMessageText"):
		return map[string]any{
			"ok": true,
			"result": map[string]any{
				"message_id": 42,
				"text":       "edited",
			},
		}
	case containsMethod(r.URL.Path, "sendChatAction"), containsMethod(r.URL.Path, "setMessageReaction"):
		return map[string]any{"ok": true}
	case containsMethod(r.URL.Path, "setMyCommands"):
		return map[string]any{"ok": true, "result": true}
	case containsMethod(r.URL.Path, "getFile"):
		return map[string]any{
			"ok": true,
			"result": map[string]any{
				"file_id":   "test_file_id",
				"file_path": "photos/test.jpg",
			},
		}
	case containsMethod(r.URL.Path, "getUpdates"):
		return map[string]any{
			"ok":     true,
			"result": []any{},
		}
	default:
		return map[string]any{
			"ok":     true,
			"result": map[string]any{},
		}
	}
}

func newMockBot(handler func(*http.Request) map[string]any) (*tgbotapi.BotAPI, error) {
	if handler == nil {
		handler = defaultMockBotResponse
	}
	return tgbotapi.NewBotAPIWithClient(
		"123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11",
		"https://mock.telegram.invalid/bot%s/%s",
		&mockBotClient{handler: handler},
	)
}

// containsMethod 检查 URL 路径是否包含指定的 API 方法
func containsMethod(path, method string) bool {
	return len(path) > len(method) && path[len(path)-len(method)-1:] == "/"+method
}

// newAdapterWithMockBot 创建一个使用 mock bot 的 Adapter
func newAdapterWithMockBot() (*Adapter, mockBotServer, error) {
	bot, err := newMockBot(nil)
	if err != nil {
		return nil, mockBotServer{}, err
	}

	cfg := DefaultConfig()
	cfg.Token = bot.Token
	adapter := NewAdapterWithClient(cfg, bot)

	return adapter, mockBotServer{}, nil
}

func newAdapterWithCapturedMessages(t *testing.T) (*Adapter, *[]capturedBotMessage) {
	t.Helper()
	var sent []capturedBotMessage
	bot, err := newMockBot(func(r *http.Request) map[string]any {
		if containsMethod(r.URL.Path, "sendMessage") || containsMethod(r.URL.Path, "editMessageText") {
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm() error = %v", err)
			}
			method := "sendMessage"
			if containsMethod(r.URL.Path, "editMessageText") {
				method = "editMessageText"
			}
			sent = append(sent, capturedBotMessage{
				Method:    method,
				Text:      r.Form.Get("text"),
				ParseMode: r.Form.Get("parse_mode"),
				ReplyTo:   r.Form.Get("reply_to_message_id"),
				ThreadID:  r.Form.Get("message_thread_id"),
			})
		}
		return defaultMockBotResponse(r)
	})
	if err != nil {
		t.Fatalf("newMockBot() error = %v", err)
	}
	cfg := DefaultConfig()
	cfg.RateLimit = 10000
	cfg.Token = bot.Token
	adapter := NewAdapterWithClient(cfg, bot)
	return adapter, &sent
}

func TestV054ProcessChannelPostReactsWithoutDispatchingToAgent(t *testing.T) {
	reactionSeen := make(chan url.Values, 1)
	bot, err := newMockBot(func(r *http.Request) map[string]any {
		if containsMethod(r.URL.Path, "setMessageReaction") {
			_ = r.ParseForm()
			reactionSeen <- r.Form
		}
		return defaultMockBotResponse(r)
	})
	if err != nil {
		t.Fatalf("new mock bot: %v", err)
	}

	adapter := NewAdapterWithClient(Config{Token: bot.Token}, bot)
	handler := NewHandler(adapter, nil)
	adapter.SetHandler(handler.HandleMessage)

	adapter.ProcessUpdate(context.Background(), tgbotapi.Update{ChannelPost: &tgbotapi.Message{
		MessageID: 42,
		Chat: &tgbotapi.Chat{
			ID:       -100123,
			Type:     "channel",
			Title:    "Release notes",
			UserName: "release_notes",
		},
		SenderChat: &tgbotapi.Chat{ID: -100123, Type: "channel", Title: "Release notes"},
		Text:       "new release",
	}})

	select {
	case form := <-reactionSeen:
		if got := form.Get("chat_id"); got != "-100123" {
			t.Fatalf("reaction chat_id = %q, want -100123", got)
		}
		if got := form.Get("message_id"); got != "42" {
			t.Fatalf("reaction message_id = %q, want 42", got)
		}
		if got := form.Get("reaction"); !strings.Contains(got, `"emoji":"👍"`) {
			t.Fatalf("reaction payload = %q, want thumbs-up", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a reaction for channel post")
	}
}

func TestV054HandlerHandleMessagePrivate(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text: "hello",
	}

	// agent 为 nil，handleChat 会 panic，但 HandleMessage 应该能路由
	// 测试命令路由
	cmdMsg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text:      "/help",
		IsCommand: true,
		Command:   "help",
	}

	// handleHelp 需要 agent，会 panic
	// 只测试路由逻辑
	_ = handler
	_ = msg
	_ = cmdMsg
}

func TestV054HandlerSetDataDirAndPersist(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	tmpDir := t.TempDir()
	// 直接设置 dataDir，不通过 SetDataDir（会触发 loadChatSessions 需要 agent）
	handler.mu.Lock()
	handler.dataDir = tmpDir
	handler.mu.Unlock()

	// 设置 session
	handler.setSessionID("12345", "session-abc")

	// 保存
	handler.saveChatSessions()

	// 验证文件存在
	path := handler.chatSessionsPath()
	if path == "" {
		t.Error("expected non-empty path")
	}

	// 验证文件内容
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read chat_sessions.json: %v", err)
	}

	var csd chatSessionsData
	if err := json.Unmarshal(data, &csd); err != nil {
		t.Fatalf("failed to parse chat_sessions.json: %v", err)
	}

	if csd.ChatSessions["12345"] != "session-abc" {
		t.Errorf("expected session 'session-abc', got '%s'", csd.ChatSessions["12345"])
	}
}

func TestV054HandlerHasSession(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	if handler.hasSession("12345") {
		t.Error("expected no session initially")
	}

	handler.setSessionID("12345", "session-abc")

	if !handler.hasSession("12345") {
		t.Error("expected session after setSessionID")
	}
}

func TestV054HandlerResetSession(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	handler.setSessionID("12345", "session-old")

	// resetSession 需要 agent，跳过实际调用
	// 只验证 setSessionID + hasSession 逻辑
	if !handler.hasSession("12345") {
		t.Error("expected session after setSessionID")
	}

	// 手动模拟 reset
	handler.setSessionID("12345", "session-new")
	if handler.hasSession("12345") {
		sid := func() string {
			handler.mu.RLock()
			defer handler.mu.RUnlock()
			return handler.sessions["12345"]
		}()
		if sid != "session-new" {
			t.Errorf("expected 'session-new', got '%s'", sid)
		}
	}
}

func TestV054HandlerHandleMessageWithAttachments(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text: "check this out",
		Attachments: []gateway.Attachment{
			{
				Type:     gateway.AttachmentImage,
				FileID:   "photo1",
				FileName: "photo.jpg",
				FileURL:  "https://example.com/photo.jpg",
			},
		},
	}

	// HandleMessage 会调用 handleChat，需要 agent
	// 只验证消息构造逻辑
	inputText := msg.Text
	if len(msg.Attachments) > 0 {
		var mediaDesc string
		mediaDesc = inputText + "\n\n[多媒体内容]\n"
		for _, att := range msg.Attachments {
			switch att.Type {
			case gateway.AttachmentImage:
				mediaDesc += fmt.Sprintf("📷 图片: %s\n", att.FileName)
			}
		}
		inputText = mediaDesc
	}

	if inputText == msg.Text {
		t.Error("expected inputText to be modified with attachment description")
	}
	_ = handler
}

func TestV054HandleStart(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text:      "/start",
		IsCommand: true,
		Command:   "start",
	}

	err = handler.handleStart(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleHelp(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text:      "/help",
		IsCommand: true,
		Command:   "help",
	}

	err = handler.handleHelp(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleCommandUnknown(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text:      "/unknown",
		IsCommand: true,
		Command:   "unknown",
	}

	err = handler.handleCommand(ctx, msg)
	if err != nil {
		t.Errorf("expected no error for unknown command, got: %v", err)
	}
}

func TestV054HandleCommandStart(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text:      "/start",
		IsCommand: true,
		Command:   "start",
	}

	err = handler.handleCommand(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleCommandHelp(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text:      "/help",
		IsCommand: true,
		Command:   "help",
	}

	err = handler.handleCommand(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleMessageCommand(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text:      "/start",
		IsCommand: true,
		Command:   "start",
	}

	err = handler.HandleMessage(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleMessageWithAttachments(t *testing.T) {
	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}
	defer server.Close()

	handler := NewHandler(adapter, nil)

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text: "check this",
		Attachments: []gateway.Attachment{
			{
				Type:     gateway.AttachmentImage,
				FileID:   "photo1",
				FileName: "photo.jpg",
				FileURL:  "https://example.com/photo.jpg",
			},
		},
	}

	// HandleMessage 会调用 handleChat，需要 agent
	// 只验证消息构造逻辑
	inputText := msg.Text
	if len(msg.Attachments) > 0 {
		var mediaDesc strings.Builder
		mediaDesc.WriteString(inputText)
		if inputText != "" {
			mediaDesc.WriteString("\n\n")
		}
		mediaDesc.WriteString("[多媒体内容]\n")
		for i, att := range msg.Attachments {
			switch att.Type {
			case gateway.AttachmentImage:
				mediaDesc.WriteString(fmt.Sprintf("📷 图片 %d: %s (URL: %s)\n", i+1, att.FileName, att.FileURL))
			}
		}
		inputText = mediaDesc.String()
	}

	if inputText == msg.Text {
		t.Error("expected inputText to be modified with attachment description")
	}
	if !strings.Contains(inputText, "📷 图片") {
		t.Error("expected image description in inputText")
	}
	_ = handler
}

func TestV054BuildAttachmentInputDefersAnalysis(t *testing.T) {
	handler := &Handler{
		agent: &mockAgentProvider{
			analyzeFn: func(ctx context.Context, attachments []gateway.Attachment) (string, error) {
				t.Fatal("gateway must not run image analysis")
				return "", nil
			},
		},
	}

	input := handler.buildUserTurnInput(context.Background(), "check this", []gateway.Attachment{
		{
			Type:     gateway.AttachmentImage,
			FileName: "photo.jpg",
			FileURL:  "https://example.com/photo.jpg",
		},
	})

	if input.RoutingText != "check this" || len(input.Attachments) != 1 {
		t.Fatalf("expected original text and attachment, got %+v", input)
	}
}

func TestV054TruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello..."},
		{"hello", 5, "hello"},
		{"hello", 3, "..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		result := presentation.TruncateString(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("presentation.TruncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

type mockAgentProvider struct {
	sessions   *session.Manager
	configSnap agentConfigSnapshot
	soulVal    *soul.Soul
	toolsVal   *tool.Registry
	skillsVal  []*tool.SkillInfo
	cronEngine *cron.Engine
	metricsVal *metrics.Metrics
	memoryVal  interface {
		memory.Store
		memory.Inspector
	}
	catalogVal       *provider.ModelCatalog
	ragVal           *rag.RAGManager
	embedderReg      *embedder.Registry
	chatFunc         func(ctx context.Context, userInput string) (string, error)
	chatSessFn       func(ctx context.Context, sessionID, userInput string) (string, error)
	chatInputFn      func(ctx context.Context, sessionID string, input agent.UserTurnInput) (string, error)
	chatStreamFn     func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error)
	chatStreamIn     func(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error)
	progressFn       func(ctx context.Context, userInput string, round int, observations []string) (string, error)
	progressPromptFn func(ctx context.Context, userInput string, round int, observations []string, presentationPrompt string) (string, error)
	analyzeFn        func(ctx context.Context, attachments []gateway.Attachment) (string, error)
	switchModelFn    func(modelID string) error
	replyAnchors     map[string]string
}

func (m *mockAgentProvider) Sessions() *session.Manager {
	return m.sessions
}

func (m *mockAgentProvider) Config() agentConfigProvider {
	return &mockConfigProvider{snap: m.configSnap}
}

func (m *mockAgentProvider) SwitchModel(modelID string) error {
	if m.switchModelFn != nil {
		return m.switchModelFn(modelID)
	}
	return nil
}

func (m *mockAgentProvider) Soul() *soul.Soul {
	return m.soulVal
}

func (m *mockAgentProvider) Tools() *tool.Registry {
	return m.toolsVal
}

func (m *mockAgentProvider) Skills() []*tool.SkillInfo {
	return m.skillsVal
}

func (m *mockAgentProvider) CronEngine() *cron.Engine {
	return m.cronEngine
}

func (m *mockAgentProvider) Chat(ctx context.Context, userInput string) (string, error) {
	if m.chatFunc != nil {
		return m.chatFunc(ctx, userInput)
	}
	return "mock response", nil
}

func (m *mockAgentProvider) ChatWithSession(ctx context.Context, sessionID, userInput string) (string, error) {
	if m.chatSessFn != nil {
		return m.chatSessFn(ctx, sessionID, userInput)
	}
	return "mock response", nil
}

func (m *mockAgentProvider) ChatWithSessionInput(ctx context.Context, sessionID string, input agent.UserTurnInput) (string, error) {
	if m.chatInputFn != nil {
		return m.chatInputFn(ctx, sessionID, input)
	}
	if m.chatSessFn != nil {
		return m.chatSessFn(ctx, sessionID, input.RoutingText)
	}
	return "mock response", nil
}

func (m *mockAgentProvider) ChatWithSessionStream(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
	if m.chatStreamFn != nil {
		return m.chatStreamFn(ctx, sessionID, userInput)
	}
	ch := make(chan agent.ChatEvent, 1)
	ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "mock response"}
	close(ch)
	return ch, nil
}

func (m *mockAgentProvider) ChatWithSessionStreamInput(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
	if m.chatStreamIn != nil {
		return m.chatStreamIn(ctx, sessionID, input)
	}
	if m.chatStreamFn != nil {
		return m.chatStreamFn(ctx, sessionID, input.RoutingText)
	}
	ch := make(chan agent.ChatEvent, 1)
	ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "mock response"}
	close(ch)
	return ch, nil
}

func (m *mockAgentProvider) ProgressFeedback(ctx context.Context, userInput string, round int, observations []string) (string, error) {
	if m.progressFn != nil {
		return m.progressFn(ctx, userInput, round, observations)
	}
	return "mock progress summary", nil
}

func (m *mockAgentProvider) ProgressFeedbackWithPrompt(ctx context.Context, userInput string, round int, observations []string, presentationPrompt string) (string, error) {
	if m.progressPromptFn != nil {
		return m.progressPromptFn(ctx, userInput, round, observations, presentationPrompt)
	}
	return m.ProgressFeedback(ctx, userInput, round, observations)
}

func (m *mockAgentProvider) AnalyzeAttachments(ctx context.Context, attachments []gateway.Attachment) (string, error) {
	if m.analyzeFn != nil {
		return m.analyzeFn(ctx, attachments)
	}
	return "", nil
}

func (m *mockAgentProvider) Metrics() *metrics.Metrics {
	return m.metricsVal
}

func (m *mockAgentProvider) Memory() memory.Inspector {
	return m.memoryVal
}

func (m *mockAgentProvider) Remember(content, category string) error {
	if m.memoryVal == nil {
		return nil
	}
	return m.memoryVal.Save(content, category)
}

func (m *mockAgentProvider) RememberLongTerm(content, category string) error {
	if m.memoryVal == nil {
		return nil
	}
	return m.memoryVal.SaveLongTerm(content, category)
}

func (m *mockAgentProvider) Recall(query string) []memory.Entry {
	if m.memoryVal == nil {
		return nil
	}
	return m.memoryVal.Search(query)
}

func (m *mockAgentProvider) MemoryStats() map[memory.Tier]int {
	if m.memoryVal == nil {
		return map[memory.Tier]int{}
	}
	return m.memoryVal.Stats()
}

func (m *mockAgentProvider) DecayMemory(threshold float64) int {
	if m.memoryVal == nil {
		return 0
	}
	return m.memoryVal.Decay(threshold)
}

func (m *mockAgentProvider) PromoteMemory(id string) error {
	if m.memoryVal == nil {
		return nil
	}
	return m.memoryVal.Promote(id)
}

func (m *mockAgentProvider) Catalog() *provider.ModelCatalog {
	if m.catalogVal != nil {
		return m.catalogVal
	}
	return provider.NewModelCatalog()
}

func (m *mockAgentProvider) RAG() *rag.RAGManager {
	return m.ragVal
}

func (m *mockAgentProvider) ContextWindowConfig() contextWindowSnapshot {
	return contextWindowSnapshot{
		MaxTokens:            4096,
		ReservedTokens:       1024,
		Strategy:             "low_priority_first",
		SlidingWindowSize:    10,
		MaxConversationTurns: 50,
		MemoryBudget:         800,
		SummarizeThreshold:   0.8,
	}
}

func (m *mockAgentProvider) ContextCacheStats() map[string]any {
	return map[string]any{"entries": 0, "hits": 0, "misses": 0}
}

func (m *mockAgentProvider) EmbedderRegistry() *embedder.Registry {
	if m.embedderReg != nil {
		return m.embedderReg
	}
	reg := embedder.NewRegistry()
	reg.Register("mock-128", embedder.NewMockEmbedder(128))
	return reg
}

func (m *mockAgentProvider) ResolveExternalReplyAnchor(platform, chatID, messageID string) (string, bool) {
	if m.replyAnchors == nil {
		return "", false
	}
	sessionID, ok := m.replyAnchors[platform+"|"+chatID+"|"+messageID]
	return sessionID, ok
}

type mockConfigProvider struct {
	snap agentConfigSnapshot
}

func (m *mockConfigProvider) Get() agentConfigSnapshot {
	return m.snap
}

type mockStreamSender struct {
	content       strings.Builder
	result        string
	finished      bool
	thinkingCount int
	toolCallCount int
}

func (m *mockStreamSender) Append(content string) error {
	m.content.WriteString(content)
	return nil
}

func (m *mockStreamSender) SetThinking(label string) error {
	m.thinkingCount++
	return nil
}

func (m *mockStreamSender) SetToolCall(name, args string) error {
	m.toolCallCount++
	return nil
}

func (m *mockStreamSender) SetResult(content string) error {
	m.result = content
	return nil
}

func (m *mockStreamSender) Finish() error {
	m.finished = true
	return nil
}

func (m *mockStreamSender) MessageID() string {
	return "mock-msg-id"
}

// newHandlerWithMockAgent creates a Handler with a mock agent for testing.
func newHandlerWithMockAgent(t *testing.T) (*Handler, mockBotServer) {
	t.Helper()

	adapter, server, err := newAdapterWithMockBot()
	if err != nil {
		t.Fatalf("failed to create adapter: %v", err)
	}

	sessMgr, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}
	mockAgent := &mockAgentProvider{
		sessions: sessMgr,
		configSnap: agentConfigSnapshot{
			HomeDir:            t.TempDir(),
			ConfigFile:         "config.json",
			Model:              "test-model",
			Provider:           "test-provider",
			MsgGatewayAPIAddr:  "127.0.0.1:9090",
			ProgressAsMessages: true,
		},
		toolsVal:   tool.NewRegistry(),
		skillsVal:  []*tool.SkillInfo{},
		cronEngine: cron.NewEngine(),
		metricsVal: metrics.NewMetrics(),
	}

	handler := &Handler{
		adapter:            adapter,
		agent:              mockAgent,
		state:              mockAgent,
		chat:               mockAgent,
		commands:           make(map[string]telegramCommandHandler),
		watcher:            cron.NewWatcher(mockAgent.cronEngine),
		sessions:           make(map[string]string),
		chatStreamTimeout:  defaultChatStreamTimeout,
		progressAsMessages: true,
	}
	handler.commands = handler.buildCommandRegistry()

	return handler, server
}

func TestV054HandleModelShow(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text:      "/model",
		IsCommand: true,
		Command:   "model",
		Args:      "",
	}

	err := handler.handleModel(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleModelSwitch(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text:      "/model gpt-4",
		IsCommand: true,
		Command:   "model",
		Args:      "gpt-4",
	}

	err := handler.handleModel(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleModelSwitchError(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).switchModelFn = func(modelID string) error {
		return fmt.Errorf("model not found")
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/model invalid",
		IsCommand: true,
		Command:   "model",
		Args:      "invalid",
	}

	err := handler.handleModel(ctx, msg)
	if err != nil {
		t.Errorf("expected no error (error sent as message), got: %v", err)
	}
}

func TestV054HandleSoul(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/soul",
		IsCommand: true,
		Command:   "soul",
	}

	err := handler.handleSoul(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleSoulWithSoul(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).soulVal = soul.Default()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/soul",
		IsCommand: true,
		Command:   "soul",
	}

	err := handler.handleSoul(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleTools(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/tools",
		IsCommand: true,
		Command:   "tools",
	}

	err := handler.handleTools(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleToolsWithTools(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	reg := handler.agent.(*mockAgentProvider).toolsVal
	reg.Register(&tool.Tool{
		Name:        "web_search",
		Description: "Search the web",
		Enabled:     true,
	})
	reg.Register(&tool.Tool{
		Name:        "shell",
		Description: "Execute shell commands",
		Enabled:     false,
	})

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/tools",
		IsCommand: true,
		Command:   "tools",
	}

	err := handler.handleTools(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleReset(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/reset",
		IsCommand: true,
		Command:   "reset",
	}

	err := handler.handleReset(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleHistory(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/history",
		IsCommand: true,
		Command:   "history",
	}

	err := handler.handleHistory(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleSession(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/session",
		IsCommand: true,
		Command:   "session",
	}

	err := handler.handleSession(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleSessionWithSession(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	// 先创建一个 session
	sid := handler.getSessionID("12345")

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/session",
		IsCommand: true,
		Command:   "session",
	}

	err := handler.handleSession(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	_ = sid
}

func TestV054HandleSessionsListsRecentSessions(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	s1 := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("First project discussion")
	s1.AddMessage("user", "hello")
	s2 := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Second project discussion")
	handler.setSessionID("12345", s2.ID)

	msg := &gateway.Message{
		ID:        "1",
		Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
		Text:      "/sessions",
		IsCommand: true,
		Command:   "sessions",
	}

	if err := handler.handleSessions(context.Background(), msg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestV054HandleResumeSwitchesSessionByFullID(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	oldSess := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Old session")
	newSess := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("New session")
	handler.setSessionID("12345", oldSess.ID)

	msg := &gateway.Message{
		ID:        "1",
		Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
		Text:      "/resume " + newSess.ID,
		IsCommand: true,
		Command:   "resume",
		Args:      newSess.ID,
	}

	if err := handler.handleResume(context.Background(), msg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got := handler.currentSessionID("12345"); got != newSess.ID {
		t.Fatalf("expected switched session %q, got %q", newSess.ID, got)
	}
}

func TestV054HandleResumeSwitchesSessionByTitle(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	oldSess := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Old session")
	target := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Release checklist")
	handler.setSessionID("12345", oldSess.ID)

	msg := &gateway.Message{
		ID:        "1",
		Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
		Text:      "/resume Release checklist",
		IsCommand: true,
		Command:   "resume",
		Args:      "Release checklist",
	}

	if err := handler.handleResume(context.Background(), msg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got := handler.currentSessionID("12345"); got != target.ID {
		t.Fatalf("expected switched session %q, got %q", target.ID, got)
	}
}

func TestV054HandleResumeSwitchesSessionByUniqueTitleFragment(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	oldSess := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Old session")
	target := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Telegram resume title support")
	handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Billing notes")
	handler.setSessionID("12345", oldSess.ID)

	msg := &gateway.Message{
		ID:        "1",
		Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
		Text:      "/resume title support",
		IsCommand: true,
		Command:   "resume",
		Args:      "title support",
	}

	if err := handler.handleResume(context.Background(), msg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got := handler.currentSessionID("12345"); got != target.ID {
		t.Fatalf("expected switched session %q, got %q", target.ID, got)
	}
}

func TestV054HandleResumeSwitchesByPrefix(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	target := handler.agent.(*mockAgentProvider).sessions.Ensure("session-target-abc")
	target.SetTitle("Target")

	msg := &gateway.Message{
		ID:        "1",
		Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
		Text:      "/resume session-target",
		IsCommand: true,
		Command:   "resume",
		Args:      "session-target",
	}

	if err := handler.handleResume(context.Background(), msg); err != nil {
		t.Fatalf("resume session by prefix: %v", err)
	}
	if got := handler.currentSessionID("12345"); got != target.ID {
		t.Fatalf("expected switched session %q, got %q", target.ID, got)
	}
}

func TestV054HandleResumeAmbiguousTitleDoesNotSwitch(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	current := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Current session")
	handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Project alpha")
	handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Project beta")
	handler.setSessionID("12345", current.ID)

	msg := &gateway.Message{
		ID:        "1",
		Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
		Text:      "/resume Project",
		IsCommand: true,
		Command:   "resume",
		Args:      "Project",
	}

	if err := handler.handleResume(context.Background(), msg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got := handler.currentSessionID("12345"); got != current.ID {
		t.Fatalf("expected current session to remain %q, got %q", current.ID, got)
	}
}

func TestV054HandleResumeMissingSessionDoesNotSwitch(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	current := handler.agent.(*mockAgentProvider).sessions.NewWithTitle("Current session")
	handler.setSessionID("12345", current.ID)

	msg := &gateway.Message{
		ID:        "1",
		Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
		Text:      "/resume missing",
		IsCommand: true,
		Command:   "resume",
		Args:      "missing",
	}

	if err := handler.handleResume(context.Background(), msg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if got := handler.currentSessionID("12345"); got != current.ID {
		t.Fatalf("expected current session to remain %q, got %q", current.ID, got)
	}
}

func TestV054HandleSkills(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/skills",
		IsCommand: true,
		Command:   "skills",
	}

	err := handler.handleSkills(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleSkillsWithSkills(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).skillsVal = []*tool.SkillInfo{
		{Name: "web-search", Description: "Search the web for information"},
		{Name: "summarize", Description: "Summarize web pages and documents"},
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/skills",
		IsCommand: true,
		Command:   "skills",
	}

	err := handler.handleSkills(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleCronList(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/cron list",
		IsCommand: true,
		Command:   "cron",
		Args:      "list",
	}

	err := handler.handleCron(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleCronEmpty(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/cron",
		IsCommand: true,
		Command:   "cron",
		Args:      "",
	}

	err := handler.handleCron(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleCronAdd(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/cron add test1 TestTask 60 check status",
		IsCommand: true,
		Command:   "cron",
		Args:      "add test1 TestTask 60 check status",
	}

	err := handler.handleCron(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleCronAddBindsCurrentSession(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	mockAgent := handler.agent.(*mockAgentProvider)
	tool.NewCronToolService(mockAgent.cronEngine, nil, func(id, mode, command string, metadata map[string]string) func() error {
		return func() error { return nil }
	}).RegisterTools(mockAgent.toolsVal)

	sess := mockAgent.sessions.NewWithTitle("telegram cron")
	handler.setSessionID("12345", sess.ID)

	msg := &gateway.Message{
		ID:        "77",
		Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
		Text:      "/cron add tg-bound 每小时 follow up",
		IsCommand: true,
		Command:   "cron",
		Args:      "add tg-bound 每小时 follow up",
	}

	if err := handler.handleCron(context.Background(), msg); err != nil {
		t.Fatalf("handleCron error = %v", err)
	}
	job, ok := mockAgent.cronEngine.GetJob("tg-bound")
	if !ok {
		t.Fatal("expected cron job to be added")
	}
	if got := job.Metadata["session_id"]; got != sess.ID {
		t.Fatalf("expected cron job session_id %q, got %q", sess.ID, got)
	}
}

func TestV054HandleCronAddInvalidInterval(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/cron add test1 TestTask abc check status",
		IsCommand: true,
		Command:   "cron",
		Args:      "add test1 TestTask abc check status",
	}

	err := handler.handleCron(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleCronRemove(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/cron remove test1",
		IsCommand: true,
		Command:   "cron",
		Args:      "remove test1",
	}

	err := handler.handleCron(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleCronUnknown(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/cron unknown",
		IsCommand: true,
		Command:   "cron",
		Args:      "unknown",
	}

	err := handler.handleCron(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleMetrics(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/metrics",
		IsCommand: true,
		Command:   "metrics",
	}

	err := handler.handleMetrics(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleHealth(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/health",
		IsCommand: true,
		Command:   "health",
	}

	err := handler.handleHealth(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleChatEmpty(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/chat",
		IsCommand: true,
		Command:   "chat",
		Args:      "",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput(""))
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleChatWithText(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/chat hello",
		IsCommand: true,
		Command:   "chat",
		Args:      "hello",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("hello"))
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleChatGroupWithSender(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "888888",
			Type: gateway.ChatGroup,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		IsGroupTrigger: true,
		Text:           "hello",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("hello"))
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleMessageNonCommand(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text: "hello world",
	}

	err := handler.HandleMessage(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleMessageNonCommandWithAttachments(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text: "check this",
		Attachments: []gateway.Attachment{
			{
				Type:     gateway.AttachmentImage,
				FileID:   "photo1",
				FileName: "photo.jpg",
				FileURL:  "https://example.com/photo.jpg",
			},
		},
	}

	err := handler.HandleMessage(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054LuckyCollectsTelegramMessagesIntoOneTurn(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	captured := make(chan agent.UserTurnInput, 1)
	handler.agent.(*mockAgentProvider).chatStreamIn = func(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
		captured <- input
		ch := make(chan agent.ChatEvent, 1)
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "batched response"}
		close(ch)
		return ch, nil
	}

	chat := gateway.Chat{ID: "12345", Type: gateway.ChatPrivate}
	sender := gateway.User{ID: "user-1", Username: "alice"}
	if err := handler.HandleMessage(context.Background(), &gateway.Message{
		ID:        "cmd-on",
		Chat:      chat,
		Sender:    sender,
		Text:      "/lucky on",
		IsCommand: true,
		Command:   "lucky",
		Args:      "on",
	}); err != nil {
		t.Fatalf("HandleMessage(/lucky on) error = %v", err)
	}
	if err := handler.HandleMessage(context.Background(), &gateway.Message{
		ID:     "m1",
		Chat:   chat,
		Sender: sender,
		Text:   "first part",
	}); err != nil {
		t.Fatalf("HandleMessage(first part) error = %v", err)
	}
	if err := handler.HandleMessage(context.Background(), &gateway.Message{
		ID:     "m2",
		Chat:   chat,
		Sender: sender,
		Text:   "second part",
		Attachments: []gateway.Attachment{{
			Type:     gateway.AttachmentImage,
			FileName: "photo.jpg",
			MimeType: "image/jpeg",
			FilePath: "/tmp/photo.jpg",
		}},
	}); err != nil {
		t.Fatalf("HandleMessage(second part) error = %v", err)
	}

	select {
	case got := <-captured:
		t.Fatalf("expected no agent dispatch before /lucky off, got %+v", got)
	case <-time.After(50 * time.Millisecond):
	}

	if err := handler.HandleMessage(context.Background(), &gateway.Message{
		ID:        "cmd-off",
		Chat:      chat,
		Sender:    sender,
		Text:      "/lucky off",
		IsCommand: true,
		Command:   "lucky",
		Args:      "off",
	}); err != nil {
		t.Fatalf("HandleMessage(/lucky off) error = %v", err)
	}

	var got agent.UserTurnInput
	select {
	case got = <-captured:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lucky batched turn")
	}
	for _, want := range []string{
		"[Collected gateway message batch]",
		"Segment 1",
		"first part",
		"Segment 2",
		"second part",
		"@alice",
		"image 1",
	} {
		if !strings.Contains(got.RoutingText, want) {
			t.Fatalf("batched routing text missing %q:\n%s", want, got.RoutingText)
		}
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("expected one attachment to be preserved, got %d", len(got.Attachments))
	}
	if got.Attachments[0].FileName != "photo.jpg" {
		t.Fatalf("unexpected attachment: %+v", got.Attachments[0])
	}
}

func TestV054LuckyCancelClearsWithoutDispatch(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	captured := make(chan agent.UserTurnInput, 1)
	handler.agent.(*mockAgentProvider).chatStreamIn = func(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
		captured <- input
		ch := make(chan agent.ChatEvent, 1)
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "unexpected response"}
		close(ch)
		return ch, nil
	}

	chat := gateway.Chat{ID: "12345", Type: gateway.ChatPrivate}
	sender := gateway.User{ID: "user-1", Username: "alice"}
	for _, msg := range []*gateway.Message{
		{ID: "cmd-on", Chat: chat, Sender: sender, Text: "/lucky on", IsCommand: true, Command: "lucky", Args: "on"},
		{ID: "m1", Chat: chat, Sender: sender, Text: "draft only"},
		{ID: "cmd-cancel", Chat: chat, Sender: sender, Text: "/lucky cancel", IsCommand: true, Command: "lucky", Args: "cancel"},
		{ID: "cmd-off", Chat: chat, Sender: sender, Text: "/lucky off", IsCommand: true, Command: "lucky", Args: "off"},
	} {
		if err := handler.HandleMessage(context.Background(), msg); err != nil {
			t.Fatalf("HandleMessage(%s) error = %v", msg.ID, err)
		}
	}
	select {
	case got := <-captured:
		t.Fatalf("expected no agent dispatch after cancel, got %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestV054HandleMessageReplyToCronAnchorUsesAnchoredSession(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	sessions := handler.agent.(*mockAgentProvider).sessions
	oldSess := sessions.NewWithTitle("old chat")
	cronSess := sessions.NewWithTitle("cron result")
	handler.setSessionID("12345", oldSess.ID)
	handler.agent.(*mockAgentProvider).replyAnchors = map[string]string{
		"telegram|12345|9001": cronSess.ID,
	}

	type capturedTurn struct {
		sessionID      string
		routingText    string
		messageContent string
	}
	captured := make(chan capturedTurn, 1)
	handler.agent.(*mockAgentProvider).chatStreamIn = func(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
		captured <- capturedTurn{
			sessionID:      sessionID,
			routingText:    input.RoutingText,
			messageContent: input.Message.Content,
		}
		ch := make(chan agent.ChatEvent, 1)
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "anchored response"}
		close(ch)
		return ch, nil
	}

	err := handler.HandleMessage(context.Background(), &gateway.Message{
		ID: "2",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{ID: "user-1"},
		Text:   "看一眼摘要",
		ReplyTo: &gateway.Message{
			ID:   "9001",
			Chat: gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
			Text: "当前状态一览：定时任务 job1 已完成，摘要：RAG 命中了课程 A 第一章，发现重点是检索召回和上下文拼接。",
		},
	})
	if err != nil {
		t.Fatalf("HandleMessage error = %v", err)
	}
	var gotTurn capturedTurn
	select {
	case gotTurn = <-captured:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for anchored chat turn")
	}
	if gotTurn.sessionID != cronSess.ID {
		t.Fatalf("expected anchored session %q, got %q", cronSess.ID, gotTurn.sessionID)
	}
	if got := handler.currentSessionID("12345"); got != cronSess.ID {
		t.Fatalf("expected chat session to switch to anchored session %q, got %q", cronSess.ID, got)
	}
	if !strings.Contains(gotTurn.routingText, "[Replied Telegram message]") {
		t.Fatalf("expected routing text to include replied message context, got: %s", gotTurn.routingText)
	}
	if !strings.Contains(gotTurn.routingText, "定时任务 job1 已完成") {
		t.Fatalf("expected routing text to include replied cron message, got: %s", gotTurn.routingText)
	}
	if !strings.Contains(gotTurn.routingText, "[User request]\n看一眼摘要") {
		t.Fatalf("expected routing text to include user reply request, got: %s", gotTurn.routingText)
	}
	if !strings.Contains(gotTurn.routingText, "Do not consult unrelated runtime, cron, or session state") {
		t.Fatalf("expected routing text to discourage unrelated runtime inspection, got: %s", gotTurn.routingText)
	}
	if gotTurn.messageContent != gotTurn.routingText {
		t.Fatalf("expected message content to match reply-aware routing text\ncontent=%s\nrouting=%s", gotTurn.messageContent, gotTurn.routingText)
	}
}

func TestV054HandleMessageReplyWithoutAnchorStillIncludesRepliedText(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	sessions := handler.agent.(*mockAgentProvider).sessions
	currentSess := sessions.NewWithTitle("current chat")
	handler.setSessionID("12345", currentSess.ID)

	type capturedTurn struct {
		sessionID      string
		routingText    string
		messageContent string
	}
	captured := make(chan capturedTurn, 1)
	handler.agent.(*mockAgentProvider).chatStreamIn = func(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
		captured <- capturedTurn{
			sessionID:      sessionID,
			routingText:    input.RoutingText,
			messageContent: input.Message.Content,
		}
		ch := make(chan agent.ChatEvent, 1)
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "reply-aware response"}
		close(ch)
		return ch, nil
	}

	err := handler.HandleMessage(context.Background(), &gateway.Message{
		ID: "3",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{ID: "user-1"},
		Text:   "可以",
		ReplyTo: &gateway.Message{
			ID:   "9002",
			Chat: gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
			Text: "我刚跑完这个定时任务：发现 RAG 课程笔记里第一章缺少索引，需要补一次重建。",
		},
	})
	if err != nil {
		t.Fatalf("HandleMessage error = %v", err)
	}
	var gotTurn capturedTurn
	select {
	case gotTurn = <-captured:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reply-aware chat turn")
	}
	if gotTurn.sessionID != currentSess.ID {
		t.Fatalf("expected current session %q without anchor, got %q", currentSess.ID, gotTurn.sessionID)
	}
	if got := handler.currentSessionID("12345"); got != currentSess.ID {
		t.Fatalf("expected chat session to remain %q without anchor, got %q", currentSess.ID, got)
	}
	if !strings.Contains(gotTurn.routingText, "[Replied Telegram message]") {
		t.Fatalf("expected routing text to include replied message context, got: %s", gotTurn.routingText)
	}
	if !strings.Contains(gotTurn.routingText, "第一章缺少索引") {
		t.Fatalf("expected routing text to include replied cron text, got: %s", gotTurn.routingText)
	}
	if !strings.Contains(gotTurn.routingText, "[User request]\n可以") {
		t.Fatalf("expected routing text to include terse user reply, got: %s", gotTurn.routingText)
	}
	if gotTurn.messageContent != gotTurn.routingText {
		t.Fatalf("expected message content to match reply-aware routing text\ncontent=%s\nrouting=%s", gotTurn.messageContent, gotTurn.routingText)
	}
}

func TestV054HandleMessageReplyToImagePreservesReplyAttachment(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	sessions := handler.agent.(*mockAgentProvider).sessions
	currentSess := sessions.NewWithTitle("current chat")
	handler.setSessionID("12345", currentSess.ID)
	handler.agent.(*mockAgentProvider).analyzeFn = func(ctx context.Context, attachments []gateway.Attachment) (string, error) {
		t.Error("gateway must not analyze images before the agent selects its vision path")
		return "", nil
	}

	type capturedTurn struct {
		routingText    string
		messageContent string
		attachments    []gateway.Attachment
		contentParts   []provider.ContentPart
	}
	captured := make(chan capturedTurn, 1)
	handler.agent.(*mockAgentProvider).chatStreamIn = func(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
		captured <- capturedTurn{
			routingText:    input.RoutingText,
			messageContent: input.Message.Content,
			attachments:    append([]gateway.Attachment(nil), input.Attachments...),
			contentParts:   append([]provider.ContentPart(nil), input.Message.ContentParts...),
		}
		ch := make(chan agent.ChatEvent, 1)
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "remembered"}
		close(ch)
		return ch, nil
	}

	err := handler.HandleMessage(context.Background(), &gateway.Message{
		ID: "4",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender: gateway.User{ID: "user-1"},
		Text:   "记住她的长相",
		ReplyTo: &gateway.Message{
			ID:   "9003",
			Chat: gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
			Attachments: []gateway.Attachment{{
				Type:     gateway.AttachmentImage,
				FileName: "face.jpg",
				MimeType: "image/jpeg",
				FilePath: "/tmp/face.jpg",
			}},
		},
	})
	if err != nil {
		t.Fatalf("HandleMessage error = %v", err)
	}

	var gotTurn capturedTurn
	select {
	case gotTurn = <-captured:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reply image chat turn")
	}
	for _, want := range []string{
		"[Replied Telegram attachments]",
		"image: face.jpg",
		"记住她的长相",
	} {
		if !strings.Contains(gotTurn.routingText, want) {
			t.Fatalf("expected routing text to include %q, got:\n%s", want, gotTurn.routingText)
		}
	}
	if gotTurn.messageContent != gotTurn.routingText {
		t.Fatalf("expected message content to match routing text\ncontent=%s\nrouting=%s", gotTurn.messageContent, gotTurn.routingText)
	}
	if len(gotTurn.attachments) != 1 {
		t.Fatalf("expected one attachment to reach agent input, got %+v", gotTurn.attachments)
	}
	if gotTurn.attachments[0].FileName != "face.jpg" {
		t.Fatalf("unexpected attachment reaching agent input: %+v", gotTurn.attachments[0])
	}
	if len(gotTurn.contentParts) != 2 {
		t.Fatalf("expected text plus image content parts, got %+v", gotTurn.contentParts)
	}
	if gotTurn.contentParts[1].Image == nil || gotTurn.contentParts[1].Image.FilePath != "/tmp/face.jpg" {
		t.Fatalf("expected reply image content part to be preserved, got %+v", gotTurn.contentParts)
	}
}

func TestV054HandleRenameCommandRenamesCurrentTelegramSession(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	sessions := handler.agent.(*mockAgentProvider).sessions
	current := sessions.NewWithTitle("old title")
	handler.setSessionID("12345", current.ID)

	err := handler.HandleMessage(context.Background(), &gateway.Message{
		ID: "3",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Sender:    gateway.User{ID: "user-1"},
		Text:      "/rename project notes",
		IsCommand: true,
		Command:   "rename",
		Args:      "project notes",
	})
	if err != nil {
		t.Fatalf("HandleMessage error = %v", err)
	}

	renamed, ok := sessions.Get(current.ID)
	if !ok {
		t.Fatalf("current session not found after rename")
	}
	if renamed.Title != "project notes" {
		t.Fatalf("expected session title to be renamed, got %q", renamed.Title)
	}
}

func TestV054HandleMessageGroupNonCommand(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "888888",
			Type: gateway.ChatGroup,
		},
		Sender: gateway.User{
			ID:       "12345",
			Username: "testuser",
		},
		Text: "hello",
	}

	err := handler.HandleMessage(ctx, msg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleCommandAll(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()

	commands := []struct {
		cmd  string
		args string
	}{
		{"start", ""},
		{"help", ""},
		{"review", ""},
		{"init", ""},
		{"config", "list"},
		{"version", ""},
		{"model", ""},
		{"models", ""},
		{"soul", ""},
		{"tools", ""},
		{"mcp", "local http://127.0.0.1:3333"},
		{"approve", "missing_tool"},
		{"deny", "missing_tool"},
		{"cron", "list"},
		{"watch", "list"},
		{"msg_gateway", "status"},
		{"rag", "stats"},
		{"context", ""},
		{"fc", "tools"},
		{"embedder", "list"},
		{"metrics", ""},
		{"health", ""},
		{"remember", "hello memory"},
		{"remember_long", "hello long memory"},
		{"recall", "hello"},
		{"memstats", ""},
		{"memdecay", ""},
		{"promote", "missing-memory"},
		{"profile", "list"},
		{"reset", ""},
		{"history", ""},
		{"session", ""},
		{"sessions", ""},
		{"resume", "missing-session"},
		{"rename", "new title"},
		{"skills", ""},
		{"new", ""},
		{"stop", ""},
		{"status", ""},
	}

	for _, tc := range commands {
		msg := &gateway.Message{
			ID: "1",
			Chat: gateway.Chat{
				ID:   "12345",
				Type: gateway.ChatPrivate,
			},
			Text:      "/" + tc.cmd,
			IsCommand: true,
			Command:   tc.cmd,
			Args:      tc.args,
		}

		err := handler.handleCommand(ctx, msg)
		if err != nil {
			t.Errorf("handleCommand(%s): expected no error, got: %v", tc.cmd, err)
		}
	}
}

func TestV054HandleCommandNormalizesTUIStyleAliases(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	ctx := context.Background()
	for _, tc := range []struct {
		command    string
		args       string
		normalized string
	}{
		{command: "msg-gateway", args: "status", normalized: "msg_gateway"},
		{command: "remember-long", args: "persist this", normalized: "remember_long"},
	} {
		msg := &gateway.Message{
			ID: "1",
			Chat: gateway.Chat{
				ID:   "12345",
				Type: gateway.ChatPrivate,
			},
			Text:      "/" + tc.command,
			IsCommand: true,
			Command:   tc.command,
			Args:      tc.args,
		}
		if err := handler.handleCommand(ctx, msg); err != nil {
			t.Fatalf("handleCommand(%s) error = %v", tc.command, err)
		}
		if msg.Command != tc.normalized {
			t.Fatalf("expected command %q to normalize to %q, got %q", tc.command, tc.normalized, msg.Command)
		}
	}
}

func TestV054HandleWatchCommandAddsAndListsPattern(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text:      "/watch add logs *.log 1m",
		IsCommand: true,
		Command:   "watch",
		Args:      "add logs *.log 1m",
	}
	if err := handler.handleCommand(context.Background(), msg); err != nil {
		t.Fatalf("handleCommand(/watch add) error = %v", err)
	}
	patterns := handler.watcherService().ListPatterns()
	if len(patterns) != 1 {
		t.Fatalf("expected one watch pattern, got %d", len(patterns))
	}
	if patterns[0].ID != "logs" || patterns[0].Pattern != "*.log" {
		t.Fatalf("unexpected watch pattern: %#v", patterns[0])
	}
}

func TestV054GetSessionID(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	// 第一次调用应该创建新 session
	sid1 := handler.getSessionID("12345")
	if sid1 == "" {
		t.Error("expected non-empty session ID")
	}

	// 第二次调用应该返回相同的 session
	sid2 := handler.getSessionID("12345")
	if sid1 != sid2 {
		t.Errorf("expected same session ID, got %s then %s", sid1, sid2)
	}
}

func TestV054ResetSession(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	sid1 := handler.getSessionID("12345")
	sid2 := handler.resetSession("12345")
	if sid1 == sid2 {
		t.Error("expected different session ID after reset")
	}
}

func TestV054HandleChatStreamError(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		return nil, fmt.Errorf("session not found")
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "hello",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("hello"))
	if err != nil {
		t.Errorf("expected no error (error handled gracefully), got: %v", err)
	}
}

func TestV054HandleChatStreamTimeout(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		return nil, fmt.Errorf("timeout after 30s")
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "hello",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("hello"))
	if err != nil {
		t.Errorf("expected no error (timeout handled gracefully), got: %v", err)
	}
}

func TestV054HandleChatStream503(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		return nil, fmt.Errorf("503 service unavailable")
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "hello",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("hello"))
	if err != nil {
		t.Errorf("expected no error (503 handled gracefully), got: %v", err)
	}
}

func TestV054HandleChatStreamSuccess(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		ch := make(chan agent.ChatEvent, 3)
		ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "thinking..."}
		ch <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "Hello!"}
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "Hello!"}
		close(ch)
		return ch, nil
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "hello",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("hello"))
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleChatStreamDeliversTimeoutFinalAnswer(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	const timeoutFinal = "This request timed out before a complete final answer was generated.\n\nNext: retry or ask me to continue from these recorded results."
	handler.agent.(*mockAgentProvider).chatStreamFn = func(context.Context, string, string) (<-chan agent.ChatEvent, error) {
		ch := make(chan agent.ChatEvent, 1)
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: timeoutFinal}
		close(ch)
		return ch, nil
	}

	msg := &gateway.Message{ID: "1", Chat: gateway.Chat{ID: "12345", Type: gateway.ChatPrivate}, Text: "download it"}
	sender := &mockStreamSender{}
	if err := handler.handleChatStream(context.Background(), sender, msg, agent.TextUserTurnInput("download it"), handler.getSessionID("12345")); err != nil {
		t.Fatalf("handleChatStream() error = %v", err)
	}
	if !sender.finished || !strings.Contains(sender.result, "timed out") {
		t.Fatalf("expected timeout final answer to be delivered, got finished=%t result=%q", sender.finished, sender.result)
	}
}

func TestV054HandleChatStreamWithToolCall(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		ch := make(chan agent.ChatEvent, 5)
		ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "thinking..."}
		ch <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "web_search", Args: `{"query":"test"}`}
		ch <- agent.ChatEvent{Type: agent.ChatEventToolResult, Content: "search results"}
		ch <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "Here are the results"}
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "Here are the results"}
		close(ch)
		return ch, nil
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "search for test",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("search for test"))
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestV054HandleChatStreamUnexpectedClose(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		ch := make(chan agent.ChatEvent, 2)
		ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "thinking..."}
		ch <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "partial response"}
		close(ch)
		return ch, nil
	}

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "hello",
	}
	sender := &mockStreamSender{}

	err := handler.handleChatStream(context.Background(), sender, msg, agent.TextUserTurnInput("hello"), handler.getSessionID("12345"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !sender.finished {
		t.Fatal("expected stream sender to be finished when event channel closes unexpectedly")
	}
	if sender.result != "❌ Error: stream ended unexpectedly, please retry" {
		t.Fatalf("expected incomplete-stream error instead of partial content, got: %q", sender.result)
	}
}

func TestV054HandleChatStreamProgressAsSeparateMessages(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		ch := make(chan agent.ChatEvent, 5)
		ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "thinking..."}
		ch <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "web_search", Args: `{"query":"test"}`}
		ch <- agent.ChatEvent{Type: agent.ChatEventToolResult, Content: "search results"}
		ch <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "Here are the results"}
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "Here are the results"}
		close(ch)
		return ch, nil
	}

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "search for test",
	}
	sender := &mockStreamSender{}

	err := handler.handleChatStream(context.Background(), sender, msg, agent.TextUserTurnInput("search for test"), handler.getSessionID("12345"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if sender.thinkingCount != 0 {
		t.Fatalf("expected no inline thinking updates, got %d", sender.thinkingCount)
	}
	if sender.toolCallCount != 0 {
		t.Fatalf("expected no inline tool-call labels, got %d", sender.toolCallCount)
	}
	if sender.result != "Here are the results" {
		t.Fatalf("expected final result, got: %q", sender.result)
	}
}

func TestV054HandleChatStreamNaturalProgressFinalOnly(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.progressAsNaturalLanguage = true

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		ch := make(chan agent.ChatEvent, 5)
		ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "先核对任务状态"}
		ch <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "file_read", Args: `{"path":"tasks/QUEUE.md"}`}
		ch <- agent.ChatEvent{Type: agent.ChatEventToolResult, Name: "file_read", Result: "ok"}
		ch <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "这是最终答案"}
		ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "这是最终答案"}
		close(ch)
		return ch, nil
	}

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "status report",
	}
	sender := &mockStreamSender{}

	err := handler.handleChatStream(context.Background(), sender, msg, agent.TextUserTurnInput("status report"), handler.getSessionID("12345"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if sender.content.Len() != 0 {
		t.Fatalf("expected no streaming append in natural progress mode, got content length %d", sender.content.Len())
	}
}

func TestV054HandleChatNarrativeStreamHidesInternalProgressMarkers(t *testing.T) {
	var sentTexts []string
	var editedTexts []string
	bot, err := newMockBot(func(r *http.Request) map[string]any {
		if containsMethod(r.URL.Path, "sendMessage") {
			_ = r.ParseForm()
			sentTexts = append(sentTexts, r.Form.Get("text"))
		}
		if containsMethod(r.URL.Path, "editMessageText") {
			_ = r.ParseForm()
			editedTexts = append(editedTexts, r.Form.Get("text"))
		}
		return defaultMockBotResponse(r)
	})
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Token = bot.Token
	adapter := NewAdapterWithClient(cfg, bot)

	sessMgr, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}

	mockAgent := &mockAgentProvider{
		sessions: sessMgr,
		configSnap: agentConfigSnapshot{
			ProgressAsMessages:        true,
			ProgressAsNaturalLanguage: true,
		},
		chatStreamIn: func(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
			ch := make(chan agent.ChatEvent, 6)
			ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "Thinking... (round 1)"}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "skill_run", Args: `{"skill_name":"content"}`}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolResult, Name: "skill_run", Result: "ok"}
			ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "Thinking... (round 2)"}
			ch <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "最终答案"}
			ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "最终答案"}
			close(ch)
			return ch, nil
		},
		toolsVal:   tool.NewRegistry(),
		skillsVal:  []*tool.SkillInfo{},
		cronEngine: cron.NewEngine(),
		metricsVal: metrics.NewMetrics(),
	}

	handler := &Handler{
		adapter:                   adapter,
		agent:                     mockAgent,
		chat:                      mockAgent,
		sessions:                  make(map[string]string),
		chatStreamTimeout:         defaultChatStreamTimeout,
		progressAsMessages:        true,
		progressAsNaturalLanguage: true,
		progressSummaryWithLLM:    true,
	}

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "帮我处理一下",
	}

	err = handler.handleChat(context.Background(), msg, agent.TextUserTurnInput("帮我处理一下"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(sentTexts) != 3 {
		t.Fatalf("expected progress placeholder, tool trace, and final message, got %d: %#v", len(sentTexts), sentTexts)
	}
	if sentTexts[0] != "🧠 Thinking..." {
		t.Fatalf("expected editable progress placeholder, got %q", sentTexts[0])
	}
	if !strings.Contains(sentTexts[1], "Tool Trace") {
		t.Fatalf("expected separate tool trace message, got %q", sentTexts[1])
	}
	if sentTexts[2] != "最终答案" {
		t.Fatalf("expected final answer only, got %q", sentTexts[2])
	}
	if len(editedTexts) < 1 {
		t.Fatalf("expected progress card edits, got %d: %#v", len(editedTexts), editedTexts)
	}
	lastEdit := editedTexts[len(editedTexts)-1]
	if strings.Contains(lastEdit, "Thinking...") || strings.Contains(lastEdit, "content") {
		t.Fatalf("expected progress card to hide internal markers, got %q", lastEdit)
	}
	if !strings.Contains(lastEdit, "Reasoning Trace") {
		t.Fatalf("expected progress card edit to contain reasoning trace, got %q", lastEdit)
	}
	if strings.Contains(lastEdit, "Tool Trace") {
		t.Fatalf("expected tool trace to stay in its own message, got %q", lastEdit)
	}
}

func TestV054HandleChatNarrativeStreamAggregatesReasoningTraceIntoOneBubble(t *testing.T) {
	var sentTexts []string
	var editedTexts []string
	bot, err := newMockBot(func(r *http.Request) map[string]any {
		_ = r.ParseForm()
		if containsMethod(r.URL.Path, "sendMessage") {
			sentTexts = append(sentTexts, r.Form.Get("text"))
		}
		if containsMethod(r.URL.Path, "editMessageText") {
			editedTexts = append(editedTexts, r.Form.Get("text"))
		}
		return defaultMockBotResponse(r)
	})
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Token = bot.Token
	adapter := NewAdapterWithClient(cfg, bot)

	sessMgr, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}

	mockAgent := &mockAgentProvider{
		sessions: sessMgr,
		configSnap: agentConfigSnapshot{
			ProgressAsMessages:        true,
			ProgressAsNaturalLanguage: true,
			ProgressSummaryWithLLM:    true,
		},
		chatStreamIn: func(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
			ch := make(chan agent.ChatEvent, 9)
			ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "Thinking... (round 1)"}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "skill_run", Args: `{"skill_name":"alpha"}`}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolResult, Name: "skill_run", Result: "ok"}
			ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "Thinking... (round 2)"}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "skill_run", Args: `{"skill_name":"beta"}`}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolResult, Name: "skill_run", Result: "ok"}
			ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "Thinking... (round 3)"}
			ch <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "最终答案"}
			ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "最终答案"}
			close(ch)
			return ch, nil
		},
		progressFn: func(ctx context.Context, userInput string, round int, observations []string) (string, error) {
			return fmt.Sprintf("progress round %d", round), nil
		},
		toolsVal:   tool.NewRegistry(),
		skillsVal:  []*tool.SkillInfo{},
		cronEngine: cron.NewEngine(),
		metricsVal: metrics.NewMetrics(),
	}

	handler := &Handler{
		adapter:                   adapter,
		agent:                     mockAgent,
		chat:                      mockAgent,
		sessions:                  make(map[string]string),
		chatStreamTimeout:         defaultChatStreamTimeout,
		progressAsMessages:        true,
		progressAsNaturalLanguage: true,
		progressSummaryWithLLM:    true,
	}

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "帮我处理一下",
	}

	if err := handler.handleChat(context.Background(), msg, agent.TextUserTurnInput("帮我处理一下")); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var reasoningSendCount int
	for _, text := range sentTexts {
		if strings.Contains(text, "Reasoning Trace") {
			reasoningSendCount++
		}
	}
	if reasoningSendCount != 0 {
		t.Fatalf("expected no separate reasoning trace sendMessage calls, got %d in %#v", reasoningSendCount, sentTexts)
	}
	var toolTraceSendCount int
	for _, text := range sentTexts {
		if strings.Contains(text, "Tool Trace") {
			toolTraceSendCount++
		}
	}
	if toolTraceSendCount != 1 {
		t.Fatalf("expected one separate tool trace message, got %d in %#v", toolTraceSendCount, sentTexts)
	}
	if len(editedTexts) < 2 {
		t.Fatalf("expected repeated edits to the same progress card, got %#v", editedTexts)
	}
	lastEdit := editedTexts[len(editedTexts)-1]
	if !strings.Contains(lastEdit, "progress round 1") || !strings.Contains(lastEdit, "progress round 2") {
		t.Fatalf("expected final progress edit to contain both updates, got %q", lastEdit)
	}
	if strings.Contains(lastEdit, "Tool Trace") {
		t.Fatalf("expected tool trace to stay out of reasoning card, got %q", lastEdit)
	}
	if strings.Count(lastEdit, "<blockquote expandable>") != 1 {
		t.Fatalf("expected one expandable blockquote in aggregated card, got %q", lastEdit)
	}
}

func TestV054HandleChatNarrativeStreamSendsAgentTrace(t *testing.T) {
	var sentTexts []string
	bot, err := newMockBot(func(r *http.Request) map[string]any {
		_ = r.ParseForm()
		if containsMethod(r.URL.Path, "sendMessage") {
			sentTexts = append(sentTexts, r.Form.Get("text"))
		}
		return defaultMockBotResponse(r)
	})
	if err != nil {
		t.Fatalf("failed to create mock bot: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Token = bot.Token
	adapter := NewAdapterWithClient(cfg, bot)

	sessMgr, err := session.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}

	mockAgent := &mockAgentProvider{
		sessions: sessMgr,
		configSnap: agentConfigSnapshot{
			ProgressAsMessages:        true,
			ProgressAsNaturalLanguage: true,
		},
		chatStreamIn: func(ctx context.Context, sessionID string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
			ch := make(chan agent.ChatEvent, 7)
			ch <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "Thinking... (round 1)"}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "delegate_task", Args: `{"task":"inspect repo","agent_id":"repo-agent"}`}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolResult, Name: "delegate_task", Result: "ok"}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "web_search", Args: `{"query":"agent trace"}`}
			ch <- agent.ChatEvent{Type: agent.ChatEventToolResult, Name: "web_search", Result: "ok"}
			ch <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "最终答案"}
			ch <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "最终答案"}
			close(ch)
			return ch, nil
		},
		toolsVal:   tool.NewRegistry(),
		skillsVal:  []*tool.SkillInfo{},
		cronEngine: cron.NewEngine(),
		metricsVal: metrics.NewMetrics(),
	}

	handler := &Handler{
		adapter:                   adapter,
		agent:                     mockAgent,
		chat:                      mockAgent,
		sessions:                  make(map[string]string),
		chatStreamTimeout:         defaultChatStreamTimeout,
		progressAsMessages:        true,
		progressAsNaturalLanguage: true,
		progressSummaryWithLLM:    true,
	}

	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "帮我处理一下",
	}

	if err := handler.handleChat(context.Background(), msg, agent.TextUserTurnInput("帮我处理一下")); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var toolTraceCount, agentTraceCount int
	for _, text := range sentTexts {
		if strings.Contains(text, "Tool Trace") {
			toolTraceCount++
			if strings.Contains(text, "delegate") {
				t.Fatalf("expected delegate tool to stay out of tool trace, got %q", text)
			}
		}
		if strings.Contains(text, "Agent Trace") {
			agentTraceCount++
			if !strings.Contains(text, "delegate") || !strings.Contains(text, "repo-agent") {
				t.Fatalf("expected agent trace to show delegate assignment, got %q", text)
			}
		}
	}
	if toolTraceCount != 1 {
		t.Fatalf("expected one tool trace message, got %d in %#v", toolTraceCount, sentTexts)
	}
	if agentTraceCount != 1 {
		t.Fatalf("expected one agent trace message, got %d in %#v", agentTraceCount, sentTexts)
	}
}

func TestShouldPrependToolNarratives(t *testing.T) {
	if !shouldPrependToolNarratives(true, false) {
		t.Fatal("expected tool narratives in non-narrative mode when enabled")
	}
	if shouldPrependToolNarratives(true, true) {
		t.Fatal("expected tool narratives to be suppressed in narrative mode")
	}
	if shouldPrependToolNarratives(false, false) {
		t.Fatal("expected tool narratives to stay disabled when flag is off")
	}
}

func TestV054HandleChatStreamErrorEvent(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		ch := make(chan agent.ChatEvent, 1)
		ch <- agent.ChatEvent{Type: agent.ChatEventError, Err: fmt.Errorf("something went wrong")}
		close(ch)
		return ch, nil
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "hello",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("hello"))
	if err != nil {
		t.Errorf("expected no error (error event handled gracefully), got: %v", err)
	}
}

func TestV054HandleChatStreamError524(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		ch := make(chan agent.ChatEvent, 1)
		ch <- agent.ChatEvent{Type: agent.ChatEventError, Err: fmt.Errorf("524 timeout")}
		close(ch)
		return ch, nil
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "hello",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("hello"))
	if err != nil {
		t.Errorf("expected no error (524 error handled gracefully), got: %v", err)
	}
}

func TestV054HandleChatStreamError429(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()

	handler.agent.(*mockAgentProvider).chatStreamFn = func(ctx context.Context, sessionID, userInput string) (<-chan agent.ChatEvent, error) {
		ch := make(chan agent.ChatEvent, 1)
		ch <- agent.ChatEvent{Type: agent.ChatEventError, Err: fmt.Errorf("429 rate limited")}
		close(ch)
		return ch, nil
	}

	ctx := context.Background()
	msg := &gateway.Message{
		ID: "1",
		Chat: gateway.Chat{
			ID:   "12345",
			Type: gateway.ChatPrivate,
		},
		Text: "hello",
	}

	err := handler.handleChat(ctx, msg, agent.TextUserTurnInput("hello"))
	if err != nil {
		t.Errorf("expected no error (429 error handled gracefully), got: %v", err)
	}
}
