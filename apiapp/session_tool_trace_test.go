package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yurika0211/luckyagent-runtime/provider"
	"github.com/yurika0211/luckyagent-runtime/session"
	"github.com/yurika0211/luckyagent-runtime/tool"
)

func TestSessionToolTraceEndpointReturnsAnnotations(t *testing.T) {
	agent := createTestAgent(t)
	server := New(agent, DefaultServerConfig())
	session := agent.Sessions().Ensure("tool-trace-session")
	session.AddProviderMessage(provider.Message{
		Role: "assistant",
		ToolCalls: []provider.ToolCall{{
			ID:        "call-read",
			Name:      "file_read",
			Arguments: `{"path":"README.md","limit":12}`,
		}},
	})
	session.AddProviderMessage(provider.Message{
		Role:       "tool",
		ToolCallID: "call-read",
		Name:       "file_read",
		Content:    "# LuckyAgent",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/tool-trace-session/tools", nil)
	writer := httptest.NewRecorder()
	server.handleSessionByID(writer, req)
	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", writer.Code, writer.Body.String())
	}

	var response struct {
		TotalCalls int `json:"total_calls"`
		Successes  int `json:"successes"`
		Tools      []struct {
			Name       string `json:"name"`
			Success    bool   `json:"success"`
			Annotation string `json:"annotation"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(writer.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.TotalCalls != 1 || response.Successes != 1 || len(response.Tools) != 1 {
		t.Fatalf("unexpected tool trace: %#v", response)
	}
	if response.Tools[0].Name != "file_read" || !response.Tools[0].Success || response.Tools[0].Annotation == "" {
		t.Fatalf("unexpected record: %#v", response.Tools[0])
	}
}

func TestSessionToolTraceRecordsApplyConfiguredTemplates(t *testing.T) {
	records := sessionToolTraceRecords([]provider.Message{{
		Role: "assistant",
		ToolCalls: []provider.ToolCall{{
			ID: "call-1", Name: "file_read", Arguments: `{"path":"README.md"}`,
		}},
	}}, map[string]string{"file_read": "检查了 {path}"})
	if len(records) != 1 || records[0].Annotation != "检查了 README.md" {
		t.Fatalf("unexpected records: %#v", records)
	}
}

func TestSessionToolTraceDistinguishesPendingAndEmptyResults(t *testing.T) {
	records := sessionToolTraceRecords([]provider.Message{
		{Role: "assistant", ToolCalls: []provider.ToolCall{
			{ID: "empty", Name: "terminal"},
			{ID: "pending", Name: "terminal"},
			{ID: "failed", Name: "file_read"},
		}},
		{Role: "tool", ToolCallID: "empty", Name: "terminal", Content: ""},
		{Role: "tool", ToolCallID: "failed", Name: "file_read", Content: "Error: not found"},
	})
	if len(records) != 3 {
		t.Fatalf("records = %#v", records)
	}
	for i, want := range []struct{ id, status string }{
		{"empty", "completed"}, {"pending", "pending"}, {"failed", "failed"},
	} {
		if records[i].ID != want.id || records[i].Status != want.status {
			t.Fatalf("record %d = %#v, want %#v", i, records[i], want)
		}
	}
	if records[1].Success {
		t.Fatal("a call without a result must not be marked successful")
	}
}

func TestSessionToolTraceFirstTurnDoesNotIncludeFollowups(t *testing.T) {
	messages := []provider.Message{
		{Role: "system", Content: "profile"},
		{Role: "user", Content: "assigned task"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "assigned", Name: "file_read"}}},
		{Role: "tool", ToolCallID: "assigned", Content: "result", DurationMS: 1250},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "follow-up"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "followup", Name: "terminal"}}},
	}
	records := sessionToolTraceRecords(firstSessionTurn(messages))
	if len(records) != 1 || records[0].ID != "assigned" || records[0].DurationMS != 1250 {
		t.Fatalf("first turn records = %#v", records)
	}
	if got := firstSessionTurn(messages[:1]); len(got) != 0 {
		t.Fatalf("session without a user turn = %#v", got)
	}
}

func TestSessionToolTracePendingDoesNotAffectSuccessRate(t *testing.T) {
	agent := createTestAgent(t)
	server := New(agent, DefaultServerConfig())
	sess := agent.Sessions().Ensure("pending-trace")
	sess.AddProviderMessage(provider.Message{Role: "user", Content: "task"})
	sess.AddProviderMessage(provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
		{ID: "done", Name: "file_read"}, {ID: "pending", Name: "terminal"},
	}})
	sess.AddProviderMessage(provider.Message{Role: "tool", ToolCallID: "done", Content: "ok"})
	sess.AddProviderMessage(provider.Message{Role: "user", Content: "follow-up"})
	sess.AddProviderMessage(provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "later", Name: "file_read"}}})
	writer := httptest.NewRecorder()
	server.handleSessionByID(writer, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/pending-trace/tools?turn=first", nil))
	var response struct {
		Turn         string             `json:"turn"`
		TotalCalls   int                `json:"total_calls"`
		PendingCalls int                `json:"pending_calls"`
		Successes    int                `json:"successes"`
		Failures     int                `json:"failures"`
		SuccessRate  float64            `json:"success_rate"`
		Tools        []tool.TraceRecord `json:"tools"`
	}
	if writer.Code != http.StatusOK || json.Unmarshal(writer.Body.Bytes(), &response) != nil {
		t.Fatalf("response = %d %s", writer.Code, writer.Body.String())
	}
	if response.Turn != "first" || response.TotalCalls != 2 || response.PendingCalls != 1 ||
		response.Successes != 1 || response.Failures != 0 || response.SuccessRate != 1 {
		t.Fatalf("response = %#v", response)
	}
	if response.Tools[1].ID != "pending" || response.Tools[1].Status != "pending" {
		t.Fatalf("tools = %#v", response.Tools)
	}
}

func TestSessionToolTraceEndpointPaginatesAndBoundsPayloads(t *testing.T) {
	agent := createTestAgent(t)
	server := New(agent, DefaultServerConfig())
	session := agent.Sessions().Ensure("tool-trace-paging-session")
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("call-%d", i)
		session.AddProviderMessage(provider.Message{
			Role: "assistant",
			ToolCalls: []provider.ToolCall{{
				ID: id, Name: "file_read", Arguments: fmt.Sprintf(`{"path":"%s"}`, strings.Repeat("x", 6000)),
			}},
		})
		session.AddProviderMessage(provider.Message{
			Role: "tool", ToolCallID: id, Name: "file_read", Content: strings.Repeat("result ", 1500),
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/tool-trace-paging-session/tools?limit=2", nil)
	writer := httptest.NewRecorder()
	server.handleSessionByID(writer, req)
	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", writer.Code, writer.Body.String())
	}
	var response struct {
		TotalCalls int                `json:"total_calls"`
		Returned   int                `json:"returned"`
		HasMore    bool               `json:"has_more"`
		Tools      []tool.TraceRecord `json:"tools"`
	}
	if err := json.NewDecoder(writer.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.TotalCalls != 4 || response.Returned != 2 || len(response.Tools) != 2 || !response.HasMore {
		t.Fatalf("unexpected page: total=%d returned=%d tools=%d has_more=%v", response.TotalCalls, response.Returned, len(response.Tools), response.HasMore)
	}
	if len([]rune(response.Tools[0].Arguments)) > traceArgumentMaxChars || len([]rune(response.Tools[0].Result)) > traceResultMaxChars {
		t.Fatalf("trace fields were not bounded: args=%d result=%d", len([]rune(response.Tools[0].Arguments)), len([]rune(response.Tools[0].Result)))
	}
}

func TestSessionToolTraceRefusesMissingOriginalTurn(t *testing.T) {
	agent := createTestAgent(t)
	server := New(agent, DefaultServerConfig())
	sess := agent.Sessions().Ensure("compacted-delegate")
	sess.AddProviderMessage(session.CompactBoundaryMessage(session.CompactMetadata{Summary: "original turn removed"}))
	sess.AddProviderMessage(provider.Message{Role: "user", Content: "follow-up"})
	sess.AddProviderMessage(provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "followup", Name: "terminal"}}})
	writer := httptest.NewRecorder()
	server.handleSessionByID(writer, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/compacted-delegate/tools?turn=first", nil))
	if writer.Code != http.StatusGone {
		t.Fatalf("missing original turn must not expose followup tools: %d %s", writer.Code, writer.Body.String())
	}
}
