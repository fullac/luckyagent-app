package qqofficial

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yurika0211/lucky-agent/agent"
	appboot "github.com/yurika0211/luckyagent-app/bootstrap"
	plugins "github.com/yurika0211/luckyagent-plugins"
	"github.com/yurika0211/luckyagent-runtime/config"
	"github.com/yurika0211/luckyagent-runtime/gateway"
	"github.com/yurika0211/luckyagent-runtime/turn"
)

type pluginCommandRuntime struct {
	*agent.Agent
	captured chan turn.Input
}

func (r *pluginCommandRuntime) ChatWithSessionStreamTurnEvents(_ context.Context, _ string, input turn.Input) (<-chan turn.ChatEvent, error) {
	r.captured <- input
	events := make(chan turn.ChatEvent, 1)
	events <- turn.ChatEvent{Type: turn.ChatEventDone, Content: "reviewed"}
	close(events)
	return events, nil
}

func TestPluginCommandReachesQQChatWithScopeAndAttachments(t *testing.T) {
	configManager, err := config.NewManagerWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configManager.Set("provider", "openai")
	configManager.Set("api_key", "test-key")
	root := t.TempDir()
	for name, content := range map[string]string{
		".lucky-plugin/plugin.json": `{"name":"starter","version":"1.0.0"}`,
		"commands/Review.md":        "Review $ARGUMENTS",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := plugins.NewManager(filepath.Join(configManager.HomeDir(), "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), root, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	instance, err := appboot.New(configManager)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	runtime := &pluginCommandRuntime{Agent: instance, captured: make(chan turn.Input, 1)}
	handler := NewHandler(&qqHandlerTestSender{}, runtime)
	defer handler.cancelAllChatTasks()
	msg := &gateway.Message{
		ID: "plugin-command", Chat: gateway.Chat{ID: "room", Type: gateway.ChatPrivate},
		Sender: gateway.User{ID: "user", Username: "alice"},
		Text:   "/starter:Review MixedCase.go --Keep-Case", IsCommand: true, Command: "starter:Review",
		Attachments: []gateway.Attachment{{Type: gateway.AttachmentImage, FileName: "diagram.png"}},
	}
	if err := handler.HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	select {
	case turnInput := <-runtime.captured:
		input := agent.UserTurnInputFromTurn(turnInput)
		if input.OriginalText != msg.Text || !strings.Contains(input.RoutingText, "MixedCase.go --Keep-Case") {
			t.Fatalf("plugin command or arguments changed: %+v", input)
		}
		if input.Scope.Platform != "qqofficial" || input.Scope.ChatID != "room" || input.Scope.SenderID != "user" || len(input.Attachments) != 1 {
			t.Fatalf("plugin command lost source or attachments: %+v", input)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("plugin command was intercepted by the built-in command router")
	}
}
