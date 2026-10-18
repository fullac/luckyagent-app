package qqofficial

import (
	"context"
	"github.com/yurika0211/lucky-agent/agent"
	"github.com/yurika0211/luckyagent-runtime/gateway"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type qqHandlerTestSender struct {
	mu            sync.Mutex
	messages      []string
	acks          []string
	forwards      []qqHandlerForwardedText
	mediaForwards []qqHandlerForwardedMedia
	events        []string
}

type qqHandlerTestStreamGateway struct {
	*qqHandlerTestSender
	stream       *qqHandlerTestStream
	chatID       string
	replyToMsgID string
}

func (s *qqHandlerTestStreamGateway) SendStream(_ context.Context, chatID string, replyToMsgID string) (gateway.StreamSender, error) {
	s.chatID = chatID
	s.replyToMsgID = replyToMsgID
	return s.stream, nil
}

type qqHandlerTestStream struct {
	content  string
	result   string
	thinking string
	toolName string
	finished bool
}

func (s *qqHandlerTestStream) Append(content string) error {
	s.content += content
	return nil
}

func (s *qqHandlerTestStream) SetThinking(label string) error {
	s.thinking = label
	return nil
}

func (s *qqHandlerTestStream) SetToolCall(name, _ string) error {
	s.toolName = name
	return nil
}

func (s *qqHandlerTestStream) SetResult(content string) error {
	s.result = content
	return nil
}

func (s *qqHandlerTestStream) Finish() error {
	s.finished = true
	return nil
}

func (s *qqHandlerTestStream) MessageID() string { return "stream-message" }

type qqHandlerForwardedText struct {
	ChatID string
	Title  string
	Chunks []string
}

type qqHandlerForwardedMedia struct {
	ChatID string
	Title  string
	Items  []gateway.ForwardedMediaItem
}

func (s *qqHandlerTestSender) Name() string { return "test" }

func (s *qqHandlerTestSender) Start(context.Context) error { return nil }

func (s *qqHandlerTestSender) Stop() error { return nil }

func (s *qqHandlerTestSender) IsRunning() bool { return true }

func (s *qqHandlerTestSender) Send(_ context.Context, _ string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	s.events = append(s.events, "text:"+message)
	return nil
}

func (s *qqHandlerTestSender) SendWithReply(_ context.Context, _ string, _ string, message string) error {
	return s.Send(context.Background(), "", message)
}

func (s *qqHandlerTestSender) SendPhoto(_ context.Context, _ string, _ string, source string, caption string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	message := strings.TrimSpace(source + " " + caption)
	s.messages = append(s.messages, message)
	s.events = append(s.events, "photo:"+message)
	return nil
}

func (s *qqHandlerTestSender) SendDocument(_ context.Context, _ string, _ string, source string, caption string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	message := strings.TrimSpace(source + " " + caption)
	s.messages = append(s.messages, message)
	s.events = append(s.events, "document:"+message)
	return nil
}

func (s *qqHandlerTestSender) AcknowledgeMessage(_ context.Context, chatID string, messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acks = append(s.acks, chatID+"|"+messageID)
	return nil
}

func (s *qqHandlerTestSender) SendForwardedText(_ context.Context, chatID string, title string, chunks []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forwards = append(s.forwards, qqHandlerForwardedText{
		ChatID: chatID,
		Title:  title,
		Chunks: append([]string(nil), chunks...),
	})
	return nil
}

func (s *qqHandlerTestSender) SendForwardedMedia(_ context.Context, chatID string, title string, items []gateway.ForwardedMediaItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "forwarded-media")
	s.mediaForwards = append(s.mediaForwards, qqHandlerForwardedMedia{
		ChatID: chatID,
		Title:  title,
		Items:  append([]gateway.ForwardedMediaItem(nil), items...),
	})
	return nil
}

func (s *qqHandlerTestSender) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.messages...)
}

func (s *qqHandlerTestSender) Events() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *qqHandlerTestSender) Acks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.acks...)
}

func (s *qqHandlerTestSender) Forwards() []qqHandlerForwardedText {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]qqHandlerForwardedText(nil), s.forwards...)
}

func (s *qqHandlerTestSender) MediaForwards() []qqHandlerForwardedMedia {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]qqHandlerForwardedMedia(nil), s.mediaForwards...)
}

func TestBuildUserTurnInputPreservesAttachments(t *testing.T) {
	h := NewHandler(NewAdapter(DefaultConfig()), nil)
	input := h.buildUserTurnInput(context.Background(), "看一下附件", []gateway.Attachment{
		{
			Type:     gateway.AttachmentImage,
			FilePath: "/tmp/example.jpg",
			FileName: "example.jpg",
			MimeType: "image/jpeg",
		},
	})

	if input.Message.Role != "user" {
		t.Fatalf("expected user role, got %q", input.Message.Role)
	}
	if len(input.Attachments) != 1 {
		t.Fatalf("expected attachment to be preserved, got %#v", input.Attachments)
	}
	if input.RoutingText == "" {
		t.Fatal("expected non-empty routing text")
	}
	if got := input.RoutingText; got != "看一下附件" {
		t.Fatalf("gateway must preserve the request and defer analysis to the agent, got %q", got)
	}
	normalized := input.Normalize()
	if len(normalized.Message.ContentParts) != 2 {
		t.Fatalf("expected text plus image content parts, got %#v", normalized.Message.ContentParts)
	}
	if normalized.Message.ContentParts[1].Image == nil || normalized.Message.ContentParts[1].Image.FilePath != "/tmp/example.jpg" {
		t.Fatalf("expected image content part to keep file path, got %#v", normalized.Message.ContentParts[1])
	}
}

func TestNapcatSenderContextTextIncludesQQAndOriginalText(t *testing.T) {
	got := napcatSenderContextText("hello", gateway.User{
		ID:       "678",
		Username: "tester",
	})

	for _, want := range []string{
		"[NapCat message sender]",
		"QQ: 678",
		"Name: @tester",
		"hello",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in sender context, got %q", want, got)
		}
	}
}

func TestInputWithMessageScopeInjectsNapcatSenderQQ(t *testing.T) {
	h := NewHandlerWithOptions(&qqHandlerTestSender{}, nil, HandlerOptions{PlatformName: "napcat"})
	input := agent.MultimodalUserTurnInput("看一下附件", []gateway.Attachment{
		{
			Type:     gateway.AttachmentImage,
			FilePath: "/tmp/example.jpg",
			MimeType: "image/jpeg",
		},
	})
	got := h.inputWithMessageScope(input, &gateway.Message{
		ID:   "msg-1",
		Chat: gateway.Chat{ID: "group:123", Type: gateway.ChatGroup},
		Sender: gateway.User{
			ID:       "3256247459",
			Username: "shiokou",
		},
	})

	if got.Scope.Platform != "napcat" || got.Scope.SenderID != "3256247459" {
		t.Fatalf("expected napcat scoped sender, got %#v", got.Scope)
	}
	for _, want := range []string{"QQ: 3256247459", "Name: @shiokou", "看一下附件"} {
		if !strings.Contains(got.RoutingText, want) {
			t.Fatalf("expected %q in routing text, got %q", want, got.RoutingText)
		}
		if !strings.Contains(got.Message.Content, want) {
			t.Fatalf("expected %q in message content, got %q", want, got.Message.Content)
		}
	}
	if len(got.Message.ContentParts) != 2 {
		t.Fatalf("expected text plus one image part, got %#v", got.Message.ContentParts)
	}
	if got.Message.ContentParts[0].Type != "text" || !strings.Contains(got.Message.ContentParts[0].Text, "QQ: 3256247459") {
		t.Fatalf("expected first content part to include sender context, got %#v", got.Message.ContentParts[0])
	}
	if got.Message.ContentParts[1].Image == nil || got.Message.ContentParts[1].Image.FilePath != "/tmp/example.jpg" {
		t.Fatalf("expected image content part to be preserved once, got %#v", got.Message.ContentParts[1])
	}
}

func TestInputWithMessageScopeDoesNotInjectQQOfficialSenderQQ(t *testing.T) {
	h := NewHandlerWithOptions(&qqHandlerTestSender{}, nil, HandlerOptions{PlatformName: "qqofficial"})
	got := h.inputWithMessageScope(agent.TextUserTurnInput("hello"), &gateway.Message{
		ID:   "msg-1",
		Chat: gateway.Chat{ID: "c2c:user-1", Type: gateway.ChatPrivate},
		Sender: gateway.User{
			ID:       "user-1",
			Username: "tester",
		},
	})

	if strings.Contains(got.RoutingText, "[NapCat message sender]") || strings.Contains(got.Message.Content, "[NapCat message sender]") {
		t.Fatalf("qqofficial input should not include napcat sender context, got %#v", got)
	}
	if got.RoutingText != "hello" || got.Message.Content != "hello" {
		t.Fatalf("expected original text to be unchanged, got routing=%q content=%q", got.RoutingText, got.Message.Content)
	}
}

func TestQQProgressHelpers(t *testing.T) {
	if got := qqThinkingMessage("Thinking... (round 2)", 2); got == "" {
		t.Fatal("expected non-empty thinking message")
	}
	if got := qqToolCallMessage("web_search", `{"query":"abc"}`); got == "" {
		t.Fatal("expected non-empty tool call message")
	}
	if got := qqToolResultMessage("web_search", "found results"); got == "" {
		t.Fatal("expected non-empty tool result message")
	}
}

func TestQQProgressTraceBuildsPublicSummary(t *testing.T) {
	trace := newQQProgressTrace()
	trace.AddThinking("Thinking... (round 1)", 1)
	trace.AddToolCall("web_search", `{"query":"abc"}`)
	trace.AddToolResult("web_search", strings.Repeat("result ", 80))

	got := trace.Message()
	if !strings.Contains(got, "本轮公开执行轨迹") {
		t.Fatalf("expected public trace title, got %q", got)
	}
	if !strings.Contains(got, "web_search") {
		t.Fatalf("expected tool name in trace, got %q", got)
	}
	if strings.Contains(got, "Thinking...") {
		t.Fatalf("trace should not expose raw thinking marker, got %q", got)
	}
	if !strings.Contains(got, "不包含模型隐藏推理") {
		t.Fatalf("expected hidden reasoning disclaimer, got %q", got)
	}
}

func TestHandlerFinalAnswerOnlySuppressesProgressMessages(t *testing.T) {
	sender := &qqHandlerTestSender{}
	handler := NewHandlerWithOptions(sender, nil, HandlerOptions{
		PlatformName:    "napcat",
		FinalAnswerOnly: true,
	})
	events := make(chan agent.ChatEvent, 5)
	events <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "Thinking... (round 1)"}
	events <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "web_search", Args: `{"query":"test"}`}
	events <- agent.ChatEvent{Type: agent.ChatEventToolResult, Name: "web_search", Result: "ok"}
	events <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "中间内容"}
	events <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "最终答案"}
	close(events)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg := &gateway.Message{
		ID:   "msg-1",
		Chat: gateway.Chat{ID: "group:123", Type: gateway.ChatGroup},
	}
	if err := handler.handleChatEventStream(ctx, msg, events); err != nil {
		t.Fatalf("handleChatEventStream() error = %v", err)
	}

	messages := sender.Messages()
	if len(messages) != 1 {
		t.Fatalf("expected exactly one outbound message, got %d: %#v", len(messages), messages)
	}
	if messages[0] != "最终答案" {
		t.Fatalf("expected final answer only, got %q", messages[0])
	}
}

func TestHandlerFinalAnswerOnlyStreamsWhenGatewaySupportsIt(t *testing.T) {
	stream := &qqHandlerTestStream{}
	sender := &qqHandlerTestStreamGateway{
		qqHandlerTestSender: &qqHandlerTestSender{},
		stream:              stream,
	}
	handler := NewHandlerWithOptions(sender, nil, HandlerOptions{
		PlatformName:    "feishu",
		FinalAnswerOnly: true,
	})
	events := make(chan agent.ChatEvent, 4)
	events <- agent.ChatEvent{Type: agent.ChatEventThinking, Content: "正在分析"}
	events <- agent.ChatEvent{Type: agent.ChatEventToolCall, Name: "web_search"}
	events <- agent.ChatEvent{Type: agent.ChatEventContent, Content: "中间内容"}
	events <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "最终答案"}
	close(events)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg := &gateway.Message{ID: "om_source", Chat: gateway.Chat{ID: "oc_stream", Type: gateway.ChatPrivate}}
	if err := handler.handleChatEventStream(ctx, msg, events); err != nil {
		t.Fatalf("handleChatEventStream() error = %v", err)
	}
	if sender.chatID != "oc_stream" || sender.replyToMsgID != "om_source" {
		t.Fatalf("stream target = chat %q reply %q", sender.chatID, sender.replyToMsgID)
	}
	if stream.thinking != "正在分析" || stream.toolName != "web_search" || stream.content != "中间内容" || stream.result != "最终答案" || !stream.finished {
		t.Fatalf("unexpected stream state: %#v", stream)
	}
	if messages := sender.Messages(); len(messages) != 0 {
		t.Fatalf("streaming gateway should not send a duplicate text response: %#v", messages)
	}
}

func TestHandlerForwardsLongAssistantText(t *testing.T) {
	sender := &qqHandlerTestSender{}
	handler := NewHandlerWithOptions(sender, nil, HandlerOptions{
		PlatformName: "napcat",
		DisplayName:  "QQ Bot",
	})
	msg := &gateway.Message{
		ID:   "msg-1",
		Chat: gateway.Chat{ID: "group:123", Type: gateway.ChatGroup},
	}
	long := strings.Repeat("你", qqLongMessageForwardThreshold+1)

	if err := handler.sendAssistantResponse(context.Background(), msg, long); err != nil {
		t.Fatalf("sendAssistantResponse() error = %v", err)
	}
	if got := sender.Messages(); len(got) != 0 {
		t.Fatalf("expected long response to skip normal messages, got %#v", got)
	}
	forwards := sender.Forwards()
	if len(forwards) != 1 {
		t.Fatalf("expected one forwarded response, got %#v", forwards)
	}
	if forwards[0].ChatID != "group:123" || forwards[0].Title != "QQ Bot" {
		t.Fatalf("unexpected forward metadata: %#v", forwards[0])
	}
	if len(forwards[0].Chunks) != 2 {
		t.Fatalf("expected long text to be split into 2 nodes, got %#v", forwards[0].Chunks)
	}
	for _, chunk := range forwards[0].Chunks {
		if qqRuneLen(chunk) > qqForwardNodeChunkLimit {
			t.Fatalf("forward chunk exceeds limit: %d", qqRuneLen(chunk))
		}
	}
}

func TestNapCatHandlerSendsMediaBeforeCompletionText(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "report.docx")
	if err := os.WriteFile(doc, []byte("docx"), 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	sender := &qqHandlerTestSender{}
	handler := NewHandlerWithOptions(sender, nil, HandlerOptions{PlatformName: "napcat"})
	msg := &gateway.Message{
		ID:   "msg-1",
		Chat: gateway.Chat{ID: "group:123", Type: gateway.ChatGroup},
	}

	if err := handler.sendAssistantResponse(context.Background(), msg, "done\nMEDIA:"+doc); err != nil {
		t.Fatalf("sendAssistantResponse() error = %v", err)
	}
	events := sender.Events()
	if len(events) != 2 || !strings.HasPrefix(events[0], "document:") || events[1] != "text:done" {
		t.Fatalf("expected document before completion text, got %#v", events)
	}
}

func TestHandlerWrapsMultiplePhotosInForwardedMedia(t *testing.T) {
	tmpDir := t.TempDir()
	img1 := tmpDir + "/one.png"
	img2 := tmpDir + "/two.jpg"
	if err := os.WriteFile(img1, []byte("one"), 0o600); err != nil {
		t.Fatalf("write first image: %v", err)
	}
	if err := os.WriteFile(img2, []byte("two"), 0o600); err != nil {
		t.Fatalf("write second image: %v", err)
	}

	sender := &qqHandlerTestSender{}
	handler := NewHandlerWithOptions(sender, nil, HandlerOptions{
		PlatformName: "napcat",
		DisplayName:  "QQ Bot",
	})
	msg := &gateway.Message{
		ID:   "msg-1",
		Chat: gateway.Chat{ID: "group:123", Type: gateway.ChatGroup},
	}
	response := "![第一张](" + img1 + ")\n![第二张](" + img2 + ")"

	if err := handler.sendAssistantResponse(context.Background(), msg, response); err != nil {
		t.Fatalf("sendAssistantResponse() error = %v", err)
	}
	if got := sender.Messages(); len(got) != 0 {
		t.Fatalf("expected forwarded media to avoid individual sends, got %#v", got)
	}
	forwards := sender.MediaForwards()
	if len(forwards) != 1 {
		t.Fatalf("expected one forwarded media payload, got %#v", forwards)
	}
	if forwards[0].ChatID != "group:123" || forwards[0].Title != "QQ Bot" {
		t.Fatalf("unexpected forward metadata: %#v", forwards[0])
	}
	if len(forwards[0].Items) != 2 {
		t.Fatalf("expected two forwarded media items, got %#v", forwards[0].Items)
	}
	if forwards[0].Items[0].Type != gateway.AttachmentImage || forwards[0].Items[0].Source != img1 || forwards[0].Items[0].Caption != "第一张" {
		t.Fatalf("unexpected first item: %#v", forwards[0].Items[0])
	}
	if forwards[0].Items[1].Type != gateway.AttachmentImage || forwards[0].Items[1].Source != img2 || forwards[0].Items[1].Caption != "第二张" {
		t.Fatalf("unexpected second item: %#v", forwards[0].Items[1])
	}
}

func TestHandlerAcknowledgesGroupMessagesOnly(t *testing.T) {
	sender := &qqHandlerTestSender{}
	handler := NewHandlerWithOptions(sender, nil, HandlerOptions{PlatformName: "napcat"})

	handler.acknowledgeIncomingMessage(&gateway.Message{
		ID:   "42",
		Chat: gateway.Chat{ID: "group:123", Type: gateway.ChatGroup},
	})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := sender.Acks(); len(got) == 1 {
			if got[0] != "group:123|42" {
				t.Fatalf("unexpected ack: %#v", got)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := sender.Acks(); len(got) != 1 {
		t.Fatalf("expected one group ack, got %#v", got)
	}

	handler.acknowledgeIncomingMessage(&gateway.Message{
		ID:   "43",
		Chat: gateway.Chat{ID: "private:3256247459", Type: gateway.ChatPrivate},
	})
	time.Sleep(50 * time.Millisecond)
	if got := sender.Acks(); len(got) != 1 {
		t.Fatalf("expected private message to skip ack, got %#v", got)
	}
}

func TestQQCommandNamesMatchTelegramCommandSet(t *testing.T) {
	expected := []string{
		"start", "help", "chat", "lucky",
		"review", "init", "config", "version", "set", "model", "models", "soul", "tools", "skills", "mcp", "approve", "deny", "cron", "watch", "msg_gateway", "rag", "context", "fc", "embedder", "metrics", "health",
		"remember", "remember_long", "recall", "memstats", "memdecay", "promote", "profile", "reset", "history", "session", "sessions", "resume", "rename", "new", "stop", "status", "restart",
	}
	got := qqCommandNames()
	if len(got) != len(expected) {
		t.Fatalf("expected %d commands, got %d: %#v", len(expected), len(got), got)
	}
	for i, want := range expected {
		if got[i] != want {
			t.Fatalf("command %d = %q, want %q", i, got[i], want)
		}
	}
}

func TestQQCommandRegistryCoversAllCommands(t *testing.T) {
	h := NewHandlerWithOptions(&qqHandlerTestSender{}, nil, HandlerOptions{PlatformName: "napcat"})
	for _, name := range qqCommandNames() {
		if h.commands[name] == nil {
			t.Fatalf("command %q is missing a handler", name)
		}
	}
}

func TestQQHelpIncludesAdvancedCommands(t *testing.T) {
	help := qqHelpMessage()
	for _, want := range []string{"/rag", "/sessions", "/remember", "/embedder", "/msg_gateway"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help is missing %s:\n%s", want, help)
		}
	}
	for _, forbidden := range []string{"`", "**", "```"} {
		if strings.Contains(help, forbidden) {
			t.Fatalf("help should be plain text, found %q in:\n%s", forbidden, help)
		}
	}
}

func TestQQProgressTraceOmitsTrivialThinkingOnlyTrace(t *testing.T) {
	trace := newQQProgressTrace()
	trace.AddThinking("Thinking... (round 1)", 1)

	if got := trace.Message(); got != "" {
		t.Fatalf("expected empty trace for trivial thinking-only turn, got %q", got)
	}
}

func TestHandlerOptionsUseCustomDeliveryGuidance(t *testing.T) {
	h := NewHandlerWithOptions(&qqHandlerTestSender{}, nil, HandlerOptions{
		PlatformName: "feishu",
		DeliveryGuidance: func(text string) string {
			return strings.TrimSpace(text) + "\n\n[Feishu delivery rule]"
		},
	})
	got := inputWithMediaDeliveryGuidance(agent.TextUserTurnInput("hello"), h.deliveryGuidance)
	if !strings.Contains(got.RoutingText, "[Feishu delivery rule]") {
		t.Fatalf("expected custom guidance, got %q", got.RoutingText)
	}
	if strings.Contains(got.RoutingText, "[QQ delivery rule]") {
		t.Fatalf("custom guidance must replace QQ guidance, got %q", got.RoutingText)
	}
}
