package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/yurika0211/luckyagent-app/appservice"
	"github.com/yurika0211/luckyagent-runtime/config"
)

// configPatchEnvelope allows clients to use a standard JSON Merge Patch body
// directly, while also providing an explicit operation for clearing secrets.
// A direct object such as {"agent":{"max_iterations":20}} is accepted too.
type configPatchEnvelope struct {
	Patch        json.RawMessage `json:"patch"`
	ClearSecrets []string        `json:"clear_secrets,omitempty"`
	IfMatch      string          `json:"if_match,omitempty"`
	DryRun       bool            `json:"dry_run,omitempty"`
}

type configPatchResponse struct {
	Config          *config.Config `json:"config"`
	Changed         []string       `json:"changed,omitempty"`
	HotReloaded     []string       `json:"hot_reloaded,omitempty"`
	RestartRequired []string       `json:"restart_required,omitempty"`
	Applied         bool           `json:"applied"`
	Persisted       bool           `json:"persisted"`
	DryRun          bool           `json:"dry_run,omitempty"`
	Version         string         `json:"version"`
}

func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil || s.agent.Config() == nil {
		s.sendError(w, "agent runtime unavailable", http.StatusServiceUnavailable, "")
		return
	}
	defer r.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&raw); err != nil {
		s.sendError(w, "invalid configuration patch", http.StatusBadRequest, "send a JSON object")
		return
	}
	patch, clearSecrets, ifMatch, dryRun, err := decodeConfigPatch(raw)
	if err != nil {
		s.sendError(w, "invalid configuration patch", http.StatusBadRequest, err.Error())
		return
	}
	for _, path := range clearSecrets {
		if !configSecretPath(path) {
			s.sendError(w, "invalid secret path", http.StatusBadRequest, "clear_secrets only accepts sensitive configuration paths")
			return
		}
	}

	current := s.agent.Config().Get()
	currentVersion := configVersion(current)
	if ifMatch == "" {
		ifMatch = strings.TrimSpace(r.Header.Get("If-Match"))
	}
	if ifMatch != "" && ifMatch != "*" && strings.Trim(ifMatch, "\"") != currentVersion {
		setConfigETag(w, currentVersion)
		s.sendError(w, "configuration changed", http.StatusConflict, "reload the current configuration and retry")
		return
	}

	submitted, err := mergeConfigPatch(current, patch, clearSecrets)
	if err != nil {
		s.sendError(w, "invalid configuration patch", http.StatusBadRequest, err.Error())
		return
	}
	service := appservice.NewConfiguration(s.agent.Config(), s.agent)
	change, err := service.Prepare(current, submitted)
	if err != nil {
		s.sendError(w, "configuration validation failed", http.StatusBadRequest, "review provider, model, and protocol settings")
		return
	}
	submitted = change.Config
	hotReloaded, restartRequired := change.HotReloaded, change.RestartRequired
	changed := patchPaths(patch)
	changed = append(changed, clearSecrets...)

	if dryRun {
		setConfigETag(w, currentVersion)
		s.sendJSON(w, http.StatusOK, configPatchResponse{
			Config:          config.RedactSecrets(submitted),
			Changed:         changed,
			HotReloaded:     hotReloaded,
			RestartRequired: restartRequired,
			DryRun:          true,
			Version:         currentVersion,
		})
		return
	}

	active, err := service.Commit(change)
	if err != nil {
		if appservice.ConfigurationErrorStage(err) == "save" {
			s.sendError(w, "configuration save failed", http.StatusInternalServerError, "")
		} else {
			message := "configuration could not be applied; previous configuration restored"
			if !appservice.ConfigurationRestored(err) {
				message = "configuration could not be applied; previous configuration could not be fully restored"
			}
			s.sendError(w, message, http.StatusBadRequest, "")
		}
		return
	}
	version := configVersion(active)
	setConfigETag(w, version)
	s.sendJSON(w, http.StatusOK, configPatchResponse{
		Config:          config.RedactSecrets(active),
		Changed:         changed,
		HotReloaded:     hotReloaded,
		RestartRequired: restartRequired,
		Applied:         true,
		Persisted:       true,
		Version:         version,
	})
}

func decodeConfigPatch(raw json.RawMessage) (json.RawMessage, []string, string, bool, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, nil, "", false, fmt.Errorf("patch must be a JSON object")
	}
	if patchRaw, ok := object["patch"]; ok {
		var envelope configPatchEnvelope
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&envelope); err != nil {
			return nil, nil, "", false, err
		}
		if len(envelope.Patch) == 0 {
			return nil, nil, "", false, fmt.Errorf("patch is required")
		}
		var patchObject map[string]json.RawMessage
		if err := json.Unmarshal(patchRaw, &patchObject); err != nil || patchObject == nil {
			return nil, nil, "", false, fmt.Errorf("patch must be a JSON object")
		}
		return patchRaw, envelope.ClearSecrets, envelope.IfMatch, envelope.DryRun, nil
	}
	return raw, nil, "", false, nil
}

func mergeConfigPatch(current *config.Config, patch json.RawMessage, clearSecrets []string) (*config.Config, error) {
	baseData, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("marshal current configuration: %w", err)
	}
	var base, changes map[string]any
	if err := json.Unmarshal(baseData, &base); err != nil {
		return nil, fmt.Errorf("decode current configuration: %w", err)
	}
	if err := json.Unmarshal(patch, &changes); err != nil || changes == nil {
		return nil, fmt.Errorf("patch must be a JSON object")
	}
	mergeJSONObjects(base, changes)
	mergedData, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("marshal merged configuration: %w", err)
	}
	var submitted config.Config
	if err := json.Unmarshal(mergedData, &submitted); err != nil {
		return nil, fmt.Errorf("decode merged configuration: %w", err)
	}
	config.PreserveRedactedSecrets(current, &submitted)
	if len(clearSecrets) > 0 {
		data, err := json.Marshal(&submitted)
		if err != nil {
			return nil, fmt.Errorf("marshal cleared configuration: %w", err)
		}
		var cleared map[string]any
		if err := json.Unmarshal(data, &cleared); err != nil {
			return nil, fmt.Errorf("decode cleared configuration: %w", err)
		}
		for _, path := range clearSecrets {
			clearJSONPath(cleared, path)
		}
		data, err = json.Marshal(cleared)
		if err != nil {
			return nil, fmt.Errorf("marshal cleared configuration: %w", err)
		}
		if err := json.Unmarshal(data, &submitted); err != nil {
			return nil, fmt.Errorf("decode cleared configuration: %w", err)
		}
	}
	return &submitted, nil
}

func mergeJSONObjects(base, changes map[string]any) {
	for key, value := range changes {
		if value == nil {
			delete(base, key)
			continue
		}
		changeObject, isObject := value.(map[string]any)
		if !isObject {
			base[key] = value
			continue
		}
		baseObject, _ := base[key].(map[string]any)
		if baseObject == nil {
			baseObject = map[string]any{}
		}
		mergeJSONObjects(baseObject, changeObject)
		base[key] = baseObject
	}
}

func clearJSONPath(object map[string]any, path string) {
	parts := strings.Split(strings.Trim(path, "."), ".")
	if len(parts) == 0 || parts[0] == "" {
		return
	}
	current := object
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	current[parts[len(parts)-1]] = nil
}

func configSecretPath(path string) bool {
	path = strings.ToLower(path)
	for _, marker := range []string{"api_key", "api_keys", "token", "secret", "password", "extra_headers"} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return false
}

func patchPaths(object json.RawMessage) []string {
	var values map[string]json.RawMessage
	if json.Unmarshal(object, &values) != nil {
		return nil
	}
	paths := make([]string, 0)
	var visit func(map[string]json.RawMessage, string)
	visit = func(current map[string]json.RawMessage, prefix string) {
		for key, value := range current {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			var nested map[string]json.RawMessage
			if json.Unmarshal(value, &nested) == nil && nested != nil {
				visit(nested, path)
				continue
			}
			paths = append(paths, path)
		}
	}
	visit(values, "")
	sort.Strings(paths)
	return paths
}

func configVersion(cfg *config.Config) string {
	data, err := json.Marshal(config.RedactSecrets(cfg))
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func setConfigETag(w http.ResponseWriter, version string) {
	if version != "" {
		w.Header().Set("ETag", `"`+version+`"`)
	}
}
