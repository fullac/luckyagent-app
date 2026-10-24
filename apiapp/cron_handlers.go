package server

import (
	"net/http"
)

func (s *Server) handleCron(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleCron(w, r)
}

func (s *Server) handleCronByID(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleCronByID(w, r)
}
