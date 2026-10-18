package server

import "net/http"

// An explicit session ID is never silently replaced by a sessionless turn.
func (s *Server) validateChatSession(w http.ResponseWriter, sessionID string) bool {
	if s.agent == nil || s.agent.Sessions() == nil {
		s.sendError(w, "agent runtime unavailable", http.StatusServiceUnavailable, "")
		return false
	}
	if sessionID != "" {
		if _, ok := s.agent.Sessions().Get(sessionID); !ok {
			s.sendError(w, "session not found", http.StatusNotFound, "")
			return false
		}
	}
	return true
}
