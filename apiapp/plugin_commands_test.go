package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appboot "github.com/yurika0211/luckyagent-app/bootstrap"
	plugins "github.com/yurika0211/luckyagent-plugins"
	"github.com/yurika0211/luckyagent-runtime/config"
)

const pluginCommandFixtureAnswer = "Plugin command completed by the local provider."

// Install the local starter bundle before application startup: discovery is a
// startup operation, and management commands do not mutate running agents.
func pluginCommandTestServer(t *testing.T) (*Server, func() []string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	var mu sync.Mutex
	var prompts []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var request struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode local provider request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if request.Model != "plugin-command-test" || request.Stream {
			t.Errorf("unexpected model/stream: model=%q stream=%t", request.Model, request.Stream)
		}
		for _, message := range request.Messages {
			if message.Role == "user" {
				mu.Lock()
				prompts = append(prompts, message.Content)
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": pluginCommandFixtureAnswer},
			}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14},
		})
	}))
	t.Cleanup(upstream.Close)

	root := filepath.Join(t.TempDir(), "starter")
	if err := os.MkdirAll(filepath.Join(root, ".lucky-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "commands"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".lucky-plugin/plugin.json": `{"name":"starter","version":"1.0.0","description":"Local starter commands for API regression tests"}`,
		"commands/review.md":        "---\ndescription: Review a file using evidence.\n---\n\nReview $ARGUMENTS. Preserve the user's spelling and report concrete findings.",
		"commands/Review-API.md":    "---\ndescription: Review an API with exact user arguments.\n---\n\nAPI review: $ARGUMENTS. Preserve every argument verbatim.",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runtimeHome := filepath.Join(home, ".luckyagent")
	mgr, err := plugins.NewManager(filepath.Join(runtimeHome, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := mgr.Install(context.Background(), root, plugins.InstallOptions{})
	if err != nil {
		t.Fatalf("install local starter: %v", err)
	}
	if !installed.Entry.Enabled || installed.Entry.Name != "starter" {
		t.Fatalf("starter was not installed and enabled: %#v", installed.Entry)
	}
	cfg, err := config.NewManagerWithDir(runtimeHome)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"provider": "openai", "api_key": "local-test-key", "api_base": upstream.URL,
		"model": "plugin-command-test", "agent.foreground.enabled": "false",
		"agent.max_iterations": "2", "agent.timeout_seconds": "5", "retry.enabled": "false",
	} {
		if err := cfg.Set(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	a, err := appboot.New(cfg)
	if err != nil {
		t.Fatalf("new agent with installed starter: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	s := New(a, DefaultServerConfig())
	t.Cleanup(func() { _ = s.Stop() })
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), prompts...)
	}
}

func TestPluginCommandCatalogIncludesStartupInstallation(t *testing.T) {
	s, captured := pluginCommandTestServer(t)
	w := httptest.NewRecorder()
	s.handleCommands(w, httptest.NewRequest(http.MethodGet, "/api/v1/commands", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d: %s", w.Code, w.Body.String())
	}
	var catalog struct {
		Commands []commandSpec `json:"commands"`
		Count    int           `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Count != len(catalog.Commands) {
		t.Fatalf("catalog count = %d, entries = %d", catalog.Count, len(catalog.Commands))
	}
	for _, expected := range []string{"starter:review", "starter:Review-API"} {
		found := false
		for _, command := range catalog.Commands {
			if command.Name == expected {
				found = command.Group == "plugins" && strings.HasPrefix(command.Usage, "/"+expected)
			}
		}
		if !found {
			t.Errorf("missing namespaced command %q: %+v", expected, catalog.Commands)
		}
	}
	if prompts := captured(); len(prompts) != 0 {
		t.Fatalf("reading command catalog invoked provider: %q", prompts)
	}
}

func TestPluginCommandChatSyncBypassesBuiltinRouterAndExpandsArguments(t *testing.T) {
	s, captured := pluginCommandTestServer(t)
	const args = "UI/HTTP-API.go --Keep-Case MixedCase-v2"
	body, err := json.Marshal(map[string]string{"message": "/starter:review " + args})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/chat/sync", bytes.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleChatSync(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("chat sync status = %d: %s", w.Code, w.Body.String())
	}
	var response ChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Response != pluginCommandFixtureAnswer || response.SessionID == "" || response.Iterations < 1 {
		t.Fatalf("plugin command did not run a real chat turn: %+v", response)
	}
	assertPluginCommandPrompt(t, captured(), "starter:review", "Review "+args+".")
}

func TestPluginCommandEndpointPreservesNamespacedCaseAndHyphens(t *testing.T) {
	s, captured := pluginCommandTestServer(t)
	const command = "starter:Review-API"
	const args = "Feature-Branch/API-v2 --Keep-Case MiXeD-Name"
	for _, suppliedCommand := range []string{command, "/" + command} {
		t.Run(suppliedCommand, func(t *testing.T) {
			sess := s.agent.Sessions().NewWithTitle("plugin-command-api")
			before := len(captured())
			body, err := json.Marshal(commandRequest{Command: suppliedCommand, Args: args, SessionID: sess.ID})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/commands", bytes.NewReader(body)).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.handleCommands(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("command status = %d: %s", w.Code, w.Body.String())
			}
			var response struct {
				Command   string `json:"command"`
				OK        bool   `json:"ok"`
				Output    string `json:"output"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !response.OK || response.Output != pluginCommandFixtureAnswer || response.Command != command || response.SessionID != sess.ID {
				t.Fatalf("namespaced command was normalized or did not run: %+v", response)
			}
			assertPluginCommandPrompt(t, captured()[before:], command, "API review: "+args+".")
		})
	}
}

func assertPluginCommandPrompt(t *testing.T, prompts []string, command, expanded string) {
	t.Helper()
	for _, prompt := range prompts {
		if strings.Contains(prompt, "[Plugin command "+command+"]") && strings.Contains(prompt, expanded) {
			if strings.Contains(prompt, "$ARGUMENTS") || strings.HasPrefix(prompt, "/"+command) {
				t.Fatalf("raw macro was sent to provider: %q", prompt)
			}
			return
		}
	}
	t.Fatalf("provider never received the expanded %q command with original argument spelling; captured prompts: %q", command, prompts)
}
