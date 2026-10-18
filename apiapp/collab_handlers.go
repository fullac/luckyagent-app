package server

import (
	"net/http"
)

func (s *Server) handleAgentsList(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsList(w, r)
}
func (s *Server) handleAgentsGet(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsGet(w, r)
}
func (s *Server) handleAgentsRegister(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsRegister(w, r)
}
func (s *Server) handleAgentsDeregister(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsDeregister(w, r)
}
func (s *Server) handleAgentsDelegate(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsDelegate(w, r)
}
func (s *Server) handleAgentsTask(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsTask(w, r)
}
func (s *Server) handleAgentsTasks(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsTasks(w, r)
}
func (s *Server) handleAgentsCancel(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsCancel(w, r)
}
func (s *Server) handleAgentsBlackboard(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsBlackboard(w, r)
}
func (s *Server) handleAgentsMessages(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleAgentsMessages(w, r)
}
