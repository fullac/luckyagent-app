package server

import (
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/yurika0211/luckyagent-runtime/config"
)

// configSchemaField describes one configurable leaf in config.Config. The
// schema deliberately uses JSON paths so clients can send the same names they
// receive from GET /api/v1/config.
type configSchemaField struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Default any    `json:"default"`
	Secret  bool   `json:"secret"`
	Apply   string `json:"apply"`
}

type configSchemaResponse struct {
	Version int                 `json:"version"`
	Fields  []configSchemaField `json:"fields"`
	Groups  map[string][]string `json:"groups"`
}

// handleConfigSchema returns the metadata needed by a configuration client to
// render and safely update the configuration editor. It does not require a
// running agent because defaults and field metadata come from the config type.
func (s *Server) handleConfigSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
		return
	}
	response := buildConfigSchema()
	s.sendJSON(w, http.StatusOK, response)
}

func buildConfigSchema() configSchemaResponse {
	defaults := config.DefaultConfig()
	fields := make([]configSchemaField, 0, 128)
	walkConfigSchema(reflect.TypeOf(*defaults), reflect.ValueOf(*defaults), "", &fields)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })

	groups := map[string][]string{
		"hot":     make([]string, 0),
		"restart": make([]string, 0),
		"unknown": make([]string, 0),
	}
	for _, field := range fields {
		groups[field.Apply] = append(groups[field.Apply], field.Path)
	}
	for _, paths := range groups {
		sort.Strings(paths)
	}
	return configSchemaResponse{Version: 1, Fields: fields, Groups: groups}
}

func walkConfigSchema(t reflect.Type, value reflect.Value, prefix string, fields *[]configSchemaField) {
	if t.Kind() == reflect.Pointer {
		if value.IsValid() && !value.IsNil() {
			walkConfigSchema(t.Elem(), value.Elem(), prefix, fields)
			return
		}
		// A pointer to a struct has no useful leaf default when it is nil. For
		// pointer scalar settings (for example auto_approve), expose the field
		// with a null default so clients can distinguish it from false/zero.
		if t.Elem().Kind() == reflect.Struct {
			return
		}
		appendConfigSchemaField(t.Elem(), reflect.Value{}, prefix, reflect.StructField{}, fields)
		return
	}
	if t.Kind() == reflect.Struct {
		for index := 0; index < t.NumField(); index++ {
			fieldType := t.Field(index)
			name := configSchemaJSONName(fieldType)
			if name == "" {
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if !configSchemaCanonicalPath(path) {
				continue
			}
			fieldValue := reflect.Value{}
			if value.IsValid() && value.Kind() == reflect.Struct && index < value.NumField() {
				fieldValue = value.Field(index)
			}
			if fieldType.Type.Kind() == reflect.Struct || (fieldType.Type.Kind() == reflect.Pointer && fieldType.Type.Elem().Kind() == reflect.Struct) {
				walkConfigSchema(fieldType.Type, fieldValue, path, fields)
				continue
			}
			appendConfigSchemaField(fieldType.Type, fieldValue, path, fieldType, fields)
		}
		return
	}
	appendConfigSchemaField(t, value, prefix, reflect.StructField{}, fields)
}

func appendConfigSchemaField(t reflect.Type, value reflect.Value, path string, field reflect.StructField, fields *[]configSchemaField) {
	if path == "" {
		return
	}
	secret := configSchemaSecret(path)
	defaultValue := configSchemaDefault(value)
	if secret {
		// A schema response must never become a side channel for credentials
		// loaded from environment variables or a local config file.
		defaultValue = nil
	}
	*fields = append(*fields, configSchemaField{
		Path:    path,
		Type:    configSchemaType(t),
		Default: defaultValue,
		Secret:  secret,
		Apply:   configSchemaApply(path),
	})
}

func configSchemaJSONName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	if name, _, _ := strings.Cut(tag, ","); name != "" {
		return name
	}
	return strings.ToLower(field.Name)
}

// These paths are legacy compatibility fields or are removed by Config's
// canonical MarshalJSON implementation. Excluding them keeps the schema in
// lockstep with GET/PUT /api/v1/config.
func configSchemaCanonicalPath(path string) bool {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "llm_provider", "extra_headers", "multimodal":
		return false
	}
	if len(parts) > 1 {
		switch parts[0] {
		case "embedding", "image_generation", "tts":
			switch parts[1] {
			case "model", "provider", "api_key", "api_base":
				return false
			}
		}
	}
	return true
}

func configSchemaType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Map, reflect.Struct:
		return "object"
	case reflect.Array, reflect.Slice:
		return "array"
	default:
		return "unknown"
	}
}

func configSchemaDefault(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if !value.CanInterface() {
		return nil
	}
	return value.Interface()
}

func configSchemaSecret(path string) bool {
	for _, part := range strings.Split(path, ".") {
		name := strings.ToLower(part)
		if strings.Contains(name, "api_key") || strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "extra_headers") {
			return true
		}
	}
	return false
}

func configSchemaApply(path string) string {
	root := strings.SplitN(path, ".", 2)[0]
	switch root {
	case "models", "fallbacks", "custom_models", "max_tokens", "temperature", "limits", "retry", "circuit_breaker", "rate_limit", "context", "context_budget", "agent", "model_router", "hooks", "tool_trace", "stream_mode":
		return "hot"
	case "server", "dashboard", "msg_gateway", "embedding", "rag", "memory", "tools", "image_generation", "tts", "autonomy", "delegate", "proactive", "web_search", "opencli", "codex", "grok", "mcp", "soul_path", "extra", "skills":
		return "restart"
	default:
		return "unknown"
	}
}
