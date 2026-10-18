package server

import (
	"github.com/yurika0211/luckyagent-api/resources"

	"net/http"

	"github.com/yurika0211/luckyagent-runtime/tool"
)

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleSkills(w, r)
}
func (s *Server) buildSkillDTOs() []skillDTO { return s.resourceController().BuildSkillDTOs() }
func (s *Server) handleSkillRoutes(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleSkillRoutes(w, r)
}
func (s *Server) handleSkillDetail(w http.ResponseWriter, name string) {
	s.resourceController().HandleSkillDetail(w, name)
}
func (s *Server) handleSkillLifecycle(w http.ResponseWriter, r *http.Request, name, action string) {
	s.resourceController().HandleSkillLifecycle(w, r, name, action)
}
func (s *Server) handleSkillReloadAll(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleSkillReloadAll(w, r)
}
func (s *Server) handleSkillUninstall(w http.ResponseWriter, r *http.Request, name string) {
	s.resourceController().HandleSkillUninstall(w, r, name)
}
func (s *Server) handleSkillVersions(w http.ResponseWriter, r *http.Request, name string) {
	s.resourceController().HandleSkillVersions(w, r, name)
}
func (s *Server) handleSkillRollback(w http.ResponseWriter, r *http.Request, name string) {
	s.resourceController().HandleSkillRollback(w, r, name)
}
func (s *Server) handleSkillInstallPrepare(w http.ResponseWriter, r *http.Request) {
	s.resourceController().HandleSkillInstallPrepare(w, r)
}
func (s *Server) stageSkillUpload(w http.ResponseWriter, r *http.Request, installer *tool.InstallManager) (string, error) {
	return s.resourceController().StageSkillUpload(w, r, installer)
}
func (s *Server) stageSkillLocalPath(r *http.Request, installer *tool.InstallManager) (string, error) {
	return s.resourceController().StageSkillLocalPath(r, installer)
}
func (s *Server) handleSkillInstalls(w http.ResponseWriter, r *http.Request, rest []string) {
	s.resourceController().HandleSkillInstalls(w, r, rest)
}

type skillDTO = resources.SkillDTO
type skillToolDTO = resources.SkillToolDTO
