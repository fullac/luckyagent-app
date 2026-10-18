package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/yurika0211/lucky-agent/agent"
	"github.com/yurika0211/luckyagent-app/appservice"
	"github.com/yurika0211/luckyagent-runtime/config"
)

type modelSwitchRequest struct {
	Kind     string `json:"kind"`
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
}

type modelProfileRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil || s.agent.Config() == nil {
		s.sendError(w, "agent runtime unavailable", http.StatusServiceUnavailable, "")
		return
	}
	switch r.Method {
	case http.MethodGet:
		current := s.agent.Config().Get()
		setConfigETag(w, configVersion(current))
		s.sendJSON(w, http.StatusOK, config.RedactSecrets(current))
	case http.MethodPut:
		s.configUpdateMu.Lock()
		defer s.configUpdateMu.Unlock()
		defer r.Body.Close()
		var submitted config.Config
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		if err := decoder.Decode(&submitted); err != nil {
			s.sendError(w, "invalid configuration payload", http.StatusBadRequest, "")
			return
		}
		current := s.agent.Config().Get()
		config.PreserveRedactedSecrets(current, &submitted)
		service := appservice.NewConfiguration(s.agent.Config(), s.agent)
		change, err := service.Prepare(current, &submitted)
		if err != nil {
			s.sendError(w, "configuration validation failed", http.StatusBadRequest, "review provider, model, and protocol settings")
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
		setConfigETag(w, configVersion(active))
		s.sendJSON(w, http.StatusOK, config.RedactSecrets(active))
	case http.MethodPatch:
		s.configUpdateMu.Lock()
		defer s.configUpdateMu.Unlock()
		s.handleConfigPatch(w, r)
	default:
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
	}
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
		return
	}
	if s.agent == nil {
		s.sendError(w, "agent runtime unavailable", http.StatusServiceUnavailable, "")
		return
	}
	var kind *config.ModelKind
	if raw := strings.TrimSpace(r.URL.Query().Get("kind")); raw != "" && !strings.EqualFold(raw, "all") {
		parsed, err := config.ParseModelKind(raw)
		if err != nil {
			s.sendError(w, err.Error(), http.StatusBadRequest, "")
			return
		}
		kind = &parsed
	}
	providerName := strings.TrimSpace(r.URL.Query().Get("provider"))
	refresh := modelRefreshRequested(r)
	result := appservice.NewModels(s.agent).List(r.Context(), appservice.ModelQuery{Kind: kind, Provider: providerName, Refresh: refresh})
	models, source := result.Models, result.Source
	discoveryError := ""
	if result.DiscoveryError != nil {
		discoveryError = result.DiscoveryError.Error()
	}
	payload := map[string]any{"models": models, "count": len(models), "source": source}
	if discoveryError != "" {
		payload["error"] = discoveryError
	}
	s.sendJSON(w, http.StatusOK, payload)
}

func modelRefreshRequested(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("refresh"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func (s *Server) handleModelSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
		return
	}
	if s.agent == nil {
		s.sendError(w, "agent runtime unavailable", http.StatusServiceUnavailable, "")
		return
	}
	defer r.Body.Close()
	var request modelSwitchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		s.sendError(w, "invalid model switch payload", http.StatusBadRequest, "")
		return
	}
	kind, err := config.ParseModelKind(request.Kind)
	if err != nil {
		s.sendError(w, err.Error(), http.StatusBadRequest, "")
		return
	}
	if err := appservice.NewModels(s.agent).Switch(kind, request.Model, agent.SwitchModelOptions{Persist: true, Provider: request.Provider}); err != nil {
		s.sendError(w, "model switch failed", http.StatusBadRequest, err.Error())
		return
	}
	current, _ := s.agent.CurrentModel(kind)
	s.sendJSON(w, http.StatusOK, current)
}

func (s *Server) handleModelProfiles(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil || s.agent.Config() == nil {
		s.sendError(w, "agent runtime unavailable", http.StatusServiceUnavailable, "")
		return
	}
	switch r.Method {
	case http.MethodGet:
		profiles := s.agent.Config().Get().Models.Profiles
		s.sendJSON(w, http.StatusOK, map[string]any{"profiles": profiles, "count": len(profiles)})
	case http.MethodPost:
		defer r.Body.Close()
		var request modelProfileRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
			s.sendError(w, "invalid model profile payload", http.StatusBadRequest, "")
			return
		}
		if err := s.agent.ApplyModelProfile(request.Name); err != nil {
			s.sendError(w, "model profile switch failed", http.StatusBadRequest, fmt.Sprint(err))
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]any{"active": s.agent.Config().Get().Models.Active, "profile": strings.TrimSpace(request.Name)})
	default:
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
	}
}
