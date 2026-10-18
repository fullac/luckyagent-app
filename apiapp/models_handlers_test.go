package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yurika0211/luckyagent-runtime/config"
)

func TestHandleConfigRedactsSecrets(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	response := httptest.NewRecorder()
	s.handleConfig(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "sk-test") {
		t.Fatalf("config endpoint exposed API key: %s", response.Body.String())
	}
}

func TestHandleConfigCanonicalRoundTripAndVisionMode(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	before := a.Config().Get().APIKey
	response := httptest.NewRecorder()
	s.handleConfig(response, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	var submitted map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	if _, exists := submitted["llm_provider"]; exists {
		t.Fatal("API returned duplicate legacy model settings")
	}
	submitted["models"].(map[string]any)["vision_mode"] = "external"
	data, err := json.Marshal(submitted)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	s.handleConfig(response, httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(data)))
	if response.Code != http.StatusOK {
		t.Fatalf("canonical config save: %d %s", response.Code, response.Body.String())
	}
	if got := a.Config().Get(); got.Models.VisionMode != "external" || got.ModelEndpoint(config.ModelKindChat).APIKey != before {
		t.Fatal("mode or redacted secret did not survive save")
	}
	submitted["models"].(map[string]any)["vision_mode"] = "both"
	data, _ = json.Marshal(submitted)
	response = httptest.NewRecorder()
	s.handleConfig(response, httptest.NewRequest(http.MethodPut, "/api/v1/config", bytes.NewReader(data)))
	if response.Code != http.StatusBadRequest || a.Config().Get().Models.VisionMode != "external" {
		t.Fatal("invalid mode was not rejected atomically")
	}
}

func TestHandleConfigPatchMergesAndReportsChanges(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	before := a.Config().Get()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/config", bytes.NewBufferString(`{"patch":{"agent":{"max_iterations":17}},"dry_run":true}`))
	s.handleConfig(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d: %s", response.Code, response.Body.String())
	}
	var preview configPatchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun || preview.Persisted || preview.Config.Agent.MaxIterations != 17 {
		t.Fatalf("unexpected dry-run response: %#v", preview)
	}
	if got := a.Config().Get().Agent.MaxIterations; got != before.Agent.MaxIterations {
		t.Fatalf("dry-run changed active config to %d", got)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPatch, "/api/v1/config", bytes.NewBufferString(`{"agent":{"max_iterations":17}}`))
	s.handleConfig(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", response.Code, response.Body.String())
	}
	var applied configPatchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || !applied.Persisted || applied.Version == "" {
		t.Fatalf("unexpected applied response: %#v", applied)
	}
	if len(applied.Changed) != 1 || applied.Changed[0] != "agent.max_iterations" {
		t.Fatalf("changed paths = %#v", applied.Changed)
	}
	if got := a.Config().Get().Agent.MaxIterations; got != 17 {
		t.Fatalf("active max_iterations = %d", got)
	}
	if got := a.Config().Get().Temperature; got != before.Temperature {
		t.Fatalf("untouched temperature changed from %v to %v", before.Temperature, got)
	}
}

func TestHandleConfigPatchClearsSecretAndRejectsStaleVersion(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	current := a.Config().Get()
	stale := configVersion(current) + "-stale"
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/config", bytes.NewBufferString(`{"patch":{"temperature":0.4}}`))
	request.Header.Set("If-Match", stale)
	s.handleConfig(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale status = %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPatch, "/api/v1/config", bytes.NewBufferString(`{"patch":{},"clear_secrets":["models.endpoints.chat.api_key"]}`))
	s.handleConfig(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("clear status = %d: %s", response.Code, response.Body.String())
	}
	if got := a.Config().Get().ModelEndpoint(config.ModelKindChat).APIKey; got != "" {
		t.Fatalf("chat API key was not cleared: %q", got)
	}
}

func TestHandleModelsRefreshQueriesProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"provider-live"}]}`))
	}))
	defer upstream.Close()

	a := createTestAgent(t)
	if err := a.Config().Set("provider", "openai-compatible"); err != nil {
		t.Fatal(err)
	}
	if err := a.Config().Set("api_base", upstream.URL+"/v1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Config().Set("model", "provider-live"); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyRuntimeConfig(a.Config().Get()); err != nil {
		t.Fatal(err)
	}
	s := New(a, DefaultServerConfig())

	plain := httptest.NewRecorder()
	s.handleModels(plain, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	if plain.Code != http.StatusOK || !strings.Contains(plain.Body.String(), "gpt-3.5-turbo") {
		t.Fatalf("plain list = %d %s", plain.Code, plain.Body.String())
	}

	refreshed := httptest.NewRecorder()
	s.handleModels(refreshed, httptest.NewRequest(http.MethodGet, "/api/v1/models?refresh=1", nil))
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh status = %d %s", refreshed.Code, refreshed.Body.String())
	}
	if !strings.Contains(refreshed.Body.String(), "provider-live") || strings.Contains(refreshed.Body.String(), "gpt-3.5-turbo") {
		t.Fatalf("refresh body = %s", refreshed.Body.String())
	}
	if !strings.Contains(refreshed.Body.String(), `"source":"provider"`) {
		t.Fatalf("source missing: %s", refreshed.Body.String())
	}
}

func TestHandleModelSwitchPersistsTypedSelection(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/models/switch", bytes.NewBufferString(`{"kind":"embedding","model":"embed-test","provider":"jina"}`))
	response := httptest.NewRecorder()
	s.handleModelSwitch(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if got := a.Config().Get().Models.Active["embedding"]; got != "embed-test" {
		t.Fatalf("active embedding model = %q", got)
	}
}
