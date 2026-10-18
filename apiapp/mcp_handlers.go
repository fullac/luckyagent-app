package server

import (
	"encoding/json"
	"net/http"
)

// MCP management accepts configured server names only. Transport and process
// settings remain in declarative configuration rather than this endpoint.
func (s *Server) handleMCPServers(w http.ResponseWriter, r *http.Request) {
	if s.mcpAdmin == nil {
		s.sendError(w, "MCP runtime unavailable", http.StatusServiceUnavailable, "")
		return
	}
	admin := s.mcpAdmin
	switch r.Method {
	case http.MethodGet:
		s.sendJSON(w, http.StatusOK, map[string]any{"servers": admin.Status()})
	case http.MethodPost:
		var request struct {
			Name   string `json:"name"`
			Action string `json:"action"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Name == "" {
			s.sendError(w, "invalid MCP action", http.StatusBadRequest, "")
			return
		}
		var err error
		switch request.Action {
		case "connect":
			err = admin.ConnectConfigured(r.Context(), request.Name)
		case "refresh":
			err = admin.Refresh(r.Context(), request.Name)
		case "disconnect":
			err = admin.Disconnect(r.Context(), request.Name)
		default:
			s.sendError(w, "unknown MCP action", http.StatusBadRequest, "")
			return
		}
		if err != nil {
			s.sendError(w, err.Error(), http.StatusBadGateway, "")
			return
		}
		s.sendJSON(w, http.StatusOK, map[string]any{"servers": admin.Status()})
	default:
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
	}
}
