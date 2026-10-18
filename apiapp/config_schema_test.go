package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildConfigSchemaIncludesDefaultsAndApplyMetadata(t *testing.T) {
	schema := buildConfigSchema()
	if schema.Version != 1 {
		t.Fatalf("schema version = %d, want 1", schema.Version)
	}
	if len(schema.Fields) == 0 {
		t.Fatal("schema has no fields")
	}
	byPath := make(map[string]configSchemaField, len(schema.Fields))
	for _, field := range schema.Fields {
		byPath[field.Path] = field
	}

	checks := []struct {
		path, typ, apply string
		defaultValue     any
		secret           bool
	}{
		{path: "agent.max_iterations", typ: "integer", apply: "hot", defaultValue: 10},
		{path: "server.addr", typ: "string", apply: "restart", defaultValue: "127.0.0.1:9090"},
		{path: "web_search.api_key", typ: "string", apply: "restart", secret: true},
		{path: "msg_gateway.telegram.token", typ: "string", apply: "restart", secret: true},
	}
	for _, check := range checks {
		field, ok := byPath[check.path]
		if !ok {
			t.Fatalf("schema is missing %q", check.path)
		}
		if field.Type != check.typ || field.Apply != check.apply || field.Secret != check.secret {
			t.Fatalf("schema[%q] = %+v", check.path, field)
		}
		if check.defaultValue != nil && field.Default != check.defaultValue {
			t.Fatalf("schema[%q].default = %#v, want %#v", check.path, field.Default, check.defaultValue)
		}
		if check.secret && field.Default != nil {
			t.Fatalf("schema[%q] exposed a secret default: %#v", check.path, field.Default)
		}
	}
	if _, ok := byPath["llm_provider.api_key"]; ok {
		t.Fatal("legacy llm_provider fields must not be exposed")
	}
	if _, ok := byPath["multimodal.provider"]; ok {
		t.Fatal("legacy multimodal fields must not be exposed")
	}
	if len(schema.Groups["hot"]) == 0 || len(schema.Groups["restart"]) == 0 {
		t.Fatalf("schema groups missing classifications: %+v", schema.Groups)
	}
}

func TestHandleConfigSchema(t *testing.T) {
	s := New(createTestAgent(t), DefaultServerConfig())
	response := httptest.NewRecorder()
	s.handleConfigSchema(response, httptest.NewRequest(http.MethodGet, "/api/v1/config/schema", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") == "" {
		t.Fatal("schema response did not set content type")
	}

	response = httptest.NewRecorder()
	s.handleConfigSchema(response, httptest.NewRequest(http.MethodPut, "/api/v1/config/schema", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}
