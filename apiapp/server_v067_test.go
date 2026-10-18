//go:build !integration
// +build !integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yurika0211/lucky-agent/agent"
)

// v0.67.0: server 包测试补全 - 覆盖 0% 函数

// TestDoChatSyncHelp 测试 doChatSync 处理 /help 命令
func TestDoChatSyncHelp(t *testing.T) {
	// 创建测试服务器
	s := &Server{}

	// 创建请求
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"message": "/help"}`))
	w := httptest.NewRecorder()

	// 调用 doChatSync
	s.doChatSync(w, req, ChatRequest{Message: "/help"}, agent.LoopConfig{}, context.Background(), time.Now())

	// 验证响应
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("doChatSync /help: expected status 200, got %d", resp.StatusCode)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	if !strings.Contains(chatResp.Response, "LuckyAgent") {
		t.Errorf("doChatSync /help: expected response to contain 'LuckyAgent', got '%s'", chatResp.Response)
	}
}

// TestDoChatSyncNew 测试 doChatSync 处理 /new 命令
func TestDoChatSyncNew(t *testing.T) {
	// 创建带 mock agent 的服务器
	a := createTestAgent(t)
	s := &Server{
		agent: a,
	}

	// 创建请求
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"message": "/new"}`))
	w := httptest.NewRecorder()

	// 调用 doChatSync
	s.doChatSync(w, req, ChatRequest{Message: "/new"}, agent.LoopConfig{}, context.Background(), time.Now())

	// 验证响应
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("doChatSync /new: expected status 200, got %d", resp.StatusCode)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	if !strings.Contains(chatResp.Response, "session") {
		t.Errorf("doChatSync /new: expected response to contain 'session', got '%s'", chatResp.Response)
	}
}

// TestDoChatSyncStatus 测试 doChatSync 处理 /status 命令
func TestDoChatSyncStatus(t *testing.T) {
	a := createTestAgent(t)
	s := &Server{
		agent: a,
	}

	// 创建请求
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"message": "/status"}`))
	w := httptest.NewRecorder()

	// 调用 doChatSync
	s.doChatSync(w, req, ChatRequest{Message: "/status"}, agent.LoopConfig{}, context.Background(), time.Now())

	// 验证响应
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("doChatSync /status: expected status 200, got %d", resp.StatusCode)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	if !strings.Contains(chatResp.Response, "Status") {
		t.Errorf("doChatSync /status: expected response to contain 'Status', got '%s'", chatResp.Response)
	}
}

// TestSendSSEError 测试 sendSSEError 函数
func TestSendSSEError(t *testing.T) {
	// 创建 buffer 和 flusher
	var buf bytes.Buffer
	flusher := &testFlusher{}

	s := &Server{}
	s.sendSSEError(&buf, flusher, "test error message")

	output := buf.String()

	// 验证 SSE 格式
	if !strings.Contains(output, "data:") {
		t.Errorf("sendSSEError: expected SSE format with 'data:', got '%s'", output)
	}

	if !strings.Contains(output, "error") {
		t.Errorf("sendSSEError: expected response to contain 'error', got '%s'", output)
	}

	if !strings.Contains(output, "test error message") {
		t.Errorf("sendSSEError: expected response to contain error message, got '%s'", output)
	}

	// 验证 flusher 被调用
	if !flusher.flushed {
		t.Error("sendSSEError: expected flusher to be called")
	}
}

// 辅助类型

// testFlusher 用于测试的 mock flusher
type testFlusher struct {
	flushed bool
}

func (f *testFlusher) Flush() {
	f.flushed = true
}

// 确保 testFlusher 实现 http.Flusher
var _ http.Flusher = (*testFlusher)(nil)
