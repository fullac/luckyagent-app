package server

import (
	"net/http"
)

func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleApprovals(w, r)
}
