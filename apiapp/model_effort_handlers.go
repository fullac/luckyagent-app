package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/yurika0211/luckyagent-app/appservice"
	"github.com/yurika0211/luckyagent-runtime/config"
)

type modelEffortRequest struct {
	Effort string `json:"effort"`
}

func modelEffortPayload(cfg *config.Config) map[string]any {
	endpoint := cfg.ModelEndpoint(config.ModelKindChat)
	selection, _ := cfg.ModelSelection(config.ModelKindChat)
	effort := config.NormalizeReasoningEffort(endpoint.ReasoningEffort)
	if effort == "" {
		effort = "auto"
	}
	supported := config.SupportsReasoningEffort(endpoint.Provider, endpoint.Protocol)
	description := "模型服务默认的思维强度；具体可用档位由模型决定。"
	if !supported {
		description = "当前 provider/protocol 未接入思维强度参数，使用模型服务默认行为。"
	}
	return map[string]any{
		"model":       selection.ID,
		"provider":    endpoint.Provider,
		"protocol":    endpoint.Protocol,
		"effort":      effort,
		"options":     config.ReasoningEffortValues(),
		"supported":   supported,
		"description": description,
	}
}

func (s *Server) handleModelEffort(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil || s.agent.Config() == nil {
		s.sendError(w, "agent runtime unavailable", http.StatusServiceUnavailable, "")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.sendJSON(w, http.StatusOK, modelEffortPayload(s.agent.Config().Get()))
	case http.MethodPut:
		s.configUpdateMu.Lock()
		defer s.configUpdateMu.Unlock()
		defer r.Body.Close()
		var request modelEffortRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
			s.sendError(w, "invalid reasoning effort payload", http.StatusBadRequest, "")
			return
		}
		effort := config.NormalizeReasoningEffort(request.Effort)
		if effort == "" {
			s.sendError(w, "unsupported reasoning effort", http.StatusBadRequest, "supported values: auto, none, minimal, low, medium, high, xhigh")
			return
		}
		current := s.agent.Config().Get()
		endpoint := current.ModelEndpoint(config.ModelKindChat)
		if effort != "auto" && !config.SupportsReasoningEffort(endpoint.Provider, endpoint.Protocol) {
			s.sendError(w, "reasoning effort is unsupported by the active provider/protocol", http.StatusBadRequest, "switch to an OpenAI or OpenAI-compatible chat endpoint first")
			return
		}
		selection, ok := current.ModelSelection(config.ModelKindChat)
		if !ok || strings.TrimSpace(selection.ID) == "" {
			s.sendError(w, "active chat model is unavailable", http.StatusBadRequest, "")
			return
		}
		next := config.Clone(current)
		nextEndpoint := next.ModelEndpoint(config.ModelKindChat)
		nextEndpoint.ReasoningEffort = effort
		if err := next.SetModelSelection(config.ModelKindChat, selection.ID, nextEndpoint); err != nil {
			s.sendError(w, "reasoning effort update failed", http.StatusBadRequest, err.Error())
			return
		}
		service := appservice.NewConfiguration(s.agent.Config(), s.agent)
		change, err := service.Prepare(current, next)
		if err != nil {
			s.sendError(w, "reasoning effort configuration rejected", http.StatusBadRequest, err.Error())
			return
		}
		if _, err := service.Commit(change); err != nil {
			if appservice.ConfigurationErrorStage(err) == "save" {
				s.sendError(w, "reasoning effort save failed", http.StatusInternalServerError, err.Error())
			} else {
				message := "reasoning effort could not be applied; previous configuration restored"
				if !appservice.ConfigurationRestored(err) {
					message = "reasoning effort could not be applied; previous configuration could not be fully restored"
				}
				s.sendError(w, message, http.StatusBadRequest, err.Error())
			}
			return
		}
		s.sendJSON(w, http.StatusOK, modelEffortPayload(s.agent.Config().Get()))
	default:
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
	}
}
