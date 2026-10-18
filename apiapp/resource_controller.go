package server

import "github.com/yurika0211/luckyagent-api/resources"

func (s *Server) resourceController() *resources.Controller {
	var backend resources.Runtime
	if s.agent != nil {
		backend = s.agent
	}
	return resources.New(resources.Options{Runtime: backend, Registry: s.collabRegistry, Delegates: s.delegateManager, SendJSON: s.sendJSON, SendError: s.sendError,
		InstallDisabled: func() bool {
			if s.agent == nil {
				return false
			}
			cfg := s.agent.Config()
			return cfg != nil && cfg.Get() != nil && cfg.Get().Skills.Install.Disabled
		},
		EnsureOrchestrator: func(id string) {
			if s.agent == nil {
				return
			}
			sess := s.agent.Sessions().Ensure(id)
			sess.SetAgentProfile("orchestrator", "You coordinate delegated collaboration tasks. Track child progress, preserve evidence, and summarize the final result clearly.")
			_ = sess.Save()
		},
	})
}
