package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yurika0211/lucky-agent/agent"
	"github.com/yurika0211/luckyagent-runtime/gateway"
)

type pluginCommandAgentProvider struct{ *mockAgentProvider }

func (*pluginCommandAgentProvider) HasPluginCommand(input string) bool {
	return strings.HasPrefix(input, "/starter:Review ")
}

func TestPluginCommandReachesTelegramChatWithScopeAndAttachments(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()
	defer handler.cancelAllChatTasks()
	provider := handler.agent.(*mockAgentProvider)
	handler.agent = &pluginCommandAgentProvider{provider}
	captured := make(chan agent.UserTurnInput, 1)
	provider.chatStreamIn = func(_ context.Context, _ string, input agent.UserTurnInput) (<-chan agent.ChatEvent, error) {
		captured <- input
		events := make(chan agent.ChatEvent, 1)
		events <- agent.ChatEvent{Type: agent.ChatEventDone, Content: "reviewed"}
		close(events)
		return events, nil
	}
	msg := &gateway.Message{
		ID: "plugin-command", Chat: gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
		Sender: gateway.User{ID: "67890", Username: "alice"},
		Text:   "/starter:Review MixedCase.go --Keep-Case", IsCommand: true, Command: "starter:Review",
		Attachments: []gateway.Attachment{{Type: gateway.AttachmentImage, FileName: "diagram.png"}},
	}
	if err := handler.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-captured:
		if input.OriginalText != msg.Text || !strings.Contains(input.RoutingText, "MixedCase.go --Keep-Case") {
			t.Fatalf("plugin command or arguments changed: %+v", input)
		}
		if input.Scope.Platform != "telegram" || input.Scope.ChatID != "12345" || input.Scope.SenderID != "67890" || len(input.Attachments) != 1 {
			t.Fatalf("plugin command lost source or attachments: %+v", input)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("plugin command was intercepted by the built-in command router")
	}
}
