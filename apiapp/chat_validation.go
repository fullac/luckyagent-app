package server

import (
	"net/http"

	"github.com/yurika0211/luckyagent-runtime/session"
)

// An explicit session ID is never silently replaced by a sessionless turn.
func (s *Server) validateChatSession(w http.ResponseWriter, sessionID string) bool {
	_, ok := s.resolveChatSession(w, sessionID)
	return ok
}

func (s *Server) resolveChatSession(w http.ResponseWriter, sessionID string) (*session.Session, bool) {
	if s.agent == nil || s.agent.Sessions() == nil {
		s.sendError(w, "agent runtime unavailable", http.StatusServiceUnavailable, "")
		return nil, false
	}
	if sessionID != "" {
		sess, ok := s.agent.Sessions().Get(sessionID)
		if !ok {
			s.sendError(w, "session not found", http.StatusNotFound, "")
			return nil, false
		}
		return sess, true
	}
	return nil, true
}
