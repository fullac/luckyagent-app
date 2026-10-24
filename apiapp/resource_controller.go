package server

import (
	"fmt"

	"github.com/yurika0211/lucky-agent/agent"
	"github.com/yurika0211/luckyagent-api/resources"
	"github.com/yurika0211/luckyagent-tools/cron"
)

// cronHTTPRuntime adapts the Agent cron methods to the API writer port.
// AddCronJob returns the stored job. Engine.GetJob copies the job, so a flag
// written through RestoreJobState is copied back onto the returned value.
type cronHTTPRuntime struct {
	*agent.Agent
}

func (a cronHTTPRuntime) AddCronJob(id, name, description, mode, command string, metadata map[string]string, schedule cron.Schedule) (*cron.Job, error) {
	if a.Agent == nil {
		return nil, fmt.Errorf("cron engine is not initialized")
	}
	if err := a.Agent.AddCronJob(id, name, description, mode, command, metadata, schedule); err != nil {
		return nil, err
	}
	job, ok := a.CronEngine().GetJob(id)
	if !ok {
		return nil, fmt.Errorf("job %s not found", id)
	}
	if metadata["delete_after_run"] == "true" || metadata["delete_after_run"] == "1" || metadata["delete_after_run"] == "yes" {
		job.DeleteAfterRun = true
	}
	return job, nil
}

func (s *Server) resourceController() *resources.Controller {
	var backend resources.Runtime
	if s.agent != nil {
		backend = cronHTTPRuntime{Agent: s.agent}
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
