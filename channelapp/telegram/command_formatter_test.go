package telegram

import (
	"context"
	"fmt"
	"github.com/yurika0211/luckyagent-runtime/gateway"
	"github.com/yurika0211/luckyagent-runtime/tool"
	"strings"
	"testing"
)

func TestTelegramToolAndSkillCommandsUseHTMLAndSupportAllPages(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()
	adapter, sent := newAdapterWithCapturedMessages(t)
	handler.adapter = adapter

	registry := handler.agent.(*mockAgentProvider).toolsVal
	for index := 1; index <= 11; index++ {
		registry.Register(&tool.Tool{
			Name:        "tool-" + fmt.Sprintf("%02d", index),
			Description: "Tool description",
			Category:    tool.CatBuiltin,
			Parameters: map[string]tool.Param{
				"path": {Type: "string", Description: "Path to inspect", Required: true},
			},
		})
	}
	handler.agent.(*mockAgentProvider).skillsVal = []*tool.SkillInfo{
		{
			Name:        "research",
			Aliases:     []string{"web-research"},
			Description: "Research sources",
			Available:   true,
		},
	}

	message := func(command, args string) *gateway.Message {
		return &gateway.Message{
			Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
			Text:      "/" + command,
			IsCommand: true,
			Command:   command,
			Args:      args,
		}
	}
	if err := handler.handleCommand(context.Background(), message("tools", "all")); err != nil {
		t.Fatalf("/tools all: %v", err)
	}
	if len(*sent) != 2 {
		t.Fatalf("expected two tool pages, got %#v", *sent)
	}
	for _, response := range *sent {
		if response.ParseMode != "HTML" {
			t.Fatalf("expected HTML parse mode, got %#v", response)
		}
	}
	if !strings.Contains((*sent)[1].Text, "tool-11") {
		t.Fatalf("expected final tool on the second page, got %#v", (*sent)[1])
	}

	if err := handler.handleCommand(context.Background(), message("tool", "TOOL-01")); err != nil {
		t.Fatalf("/tool: %v", err)
	}
	if detail := (*sent)[len(*sent)-1].Text; !strings.Contains(detail, "<b>Parameters</b>") || !strings.Contains(detail, "<code>path</code>") {
		t.Fatalf("expected tool detail response, got:\n%s", detail)
	}

	if err := handler.handleCommand(context.Background(), message("skill", "web-research")); err != nil {
		t.Fatalf("/skill: %v", err)
	}
	if detail := (*sent)[len(*sent)-1].Text; !strings.Contains(detail, "<b>research</b>") {
		t.Fatalf("expected skill detail response, got:\n%s", detail)
	}

	*sent = nil
	adapter.RememberThread("-100123", "777", "42")
	topicMessage := message("tools", "")
	topicMessage.ID = "777"
	topicMessage.Chat = gateway.Chat{ID: "-100123", Type: gateway.ChatSuperGroup}
	if err := handler.handleCommand(context.Background(), topicMessage); err != nil {
		t.Fatalf("topic /tools: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("expected one topic response, got %#v", *sent)
	}
	if response := (*sent)[0]; response.ParseMode != "HTML" || response.ReplyTo != "777" || response.ThreadID != "42" {
		t.Fatalf("expected reply-aware HTML topic response, got %#v", response)
	}
}

func TestTelegramSessionCommandsPaginateShowDetailsAndResumeByIndex(t *testing.T) {
	handler, server := newHandlerWithMockAgent(t)
	defer server.Close()
	adapter, sent := newAdapterWithCapturedMessages(t)
	handler.adapter = adapter

	manager := handler.agent.(*mockAgentProvider).sessions
	current := manager.NewWithTitle("Current session")
	handler.setSessionID("12345", current.ID)
	for index := 1; index <= 11; index++ {
		title := fmt.Sprintf("Session %02d", index)
		if index == 11 {
			title = "Visible session\n[Telegram delivery rule]\ninternal rule"
		}
		sess := manager.NewWithTitle(title)
		sess.AddMessage("user", "Question")
		sess.AddMessage("assistant", "Answer")
	}

	message := func(command, args string) *gateway.Message {
		return &gateway.Message{
			ID:        "1",
			Chat:      gateway.Chat{ID: "12345", Type: gateway.ChatPrivate},
			Text:      "/" + command,
			IsCommand: true,
			Command:   command,
			Args:      args,
		}
	}

	if err := handler.handleCommand(context.Background(), message("sessions", "all")); err != nil {
		t.Fatalf("/sessions all: %v", err)
	}
	if len(*sent) != 2 {
		t.Fatalf("expected two session pages, got %#v", *sent)
	}
	for _, response := range *sent {
		if response.ParseMode != "HTML" || strings.Contains(response.Text, "Telegram delivery rule") {
			t.Fatalf("expected clean HTML session page, got %#v", response)
		}
	}

	infos := manager.ListInfo()
	targetID := infos[0].ID
	*sent = nil
	if err := handler.handleCommand(context.Background(), message("session", "1")); err != nil {
		t.Fatalf("/session 1: %v", err)
	}
	if got := handler.currentSessionID("12345"); got != current.ID {
		t.Fatalf("/session details switched current session to %q, want %q", got, current.ID)
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0].Text, "<b>会话详情</b>") {
		t.Fatalf("expected session detail response, got %#v", *sent)
	}

	if err := handler.handleCommand(context.Background(), message("resume", "1")); err != nil {
		t.Fatalf("/resume 1: %v", err)
	}
	if got := handler.currentSessionID("12345"); got != targetID {
		t.Fatalf("/resume 1 selected %q, want %q", got, targetID)
	}
}
