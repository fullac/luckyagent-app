package server

import (
	"net/http"
	"strings"

	"github.com/yurika0211/lucky-agent/agent"
)

func (s *Server) handlePluginCommand(w http.ResponseWriter, r *http.Request, req commandRequest) {
	id := strings.TrimSpace(req.SessionID)
	sess, exists := s.agent.Sessions().Get(id)
	if id == "" {
		sess = s.agent.Sessions().NewWithTitle("plugin-command")
		id = sess.ID
	} else if !exists {
		s.sendError(w, "session not found", http.StatusNotFound, "")
		return
	}
	cfg := agent.DefaultLoopConfig()
	agent.ApplyAgentLoopConfig(&cfg, s.agent.Config().Get().Agent)
	cfg.Source = "http"
	name := strings.TrimPrefix(strings.TrimSpace(req.Command), "/")
	result, err := s.agent.RunLoopWithSession(r.Context(), sess, "/"+name+" "+req.Args, cfg)
	if err != nil {
		s.sendJSON(w, http.StatusOK, map[string]any{"command": name, "ok": false, "output": err.Error(), "session_id": id})
		return
	}
	s.sendJSON(w, http.StatusOK, map[string]any{"command": name, "ok": true, "output": result.Response, "session_id": id})
}
