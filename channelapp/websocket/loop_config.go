package websocket

import (
	"context"

	"github.com/yurika0211/lucky-agent/agent"
	"github.com/yurika0211/luckyagent-runtime/session"
)

type loopConfigRuntime interface {
	RunLoopWithSessionInput(context.Context, *session.Session, agent.UserTurnInput, agent.LoopConfig) (*agent.LoopResult, error)
	ChatWithSessionStreamInputWithLoopConfig(context.Context, string, agent.UserTurnInput, agent.LoopConfig) (<-chan agent.ChatEvent, error)
}

func (h *AgentHandler) chatLoopConfig(data ChatData) agent.LoopConfig {
	cfg := agent.DefaultLoopConfig()
	if runtime, ok := h.agent.(runtimeConfigProvider); ok && runtime.Config() != nil {
		agent.ApplyAgentLoopConfig(&cfg, runtime.Config().Get().Agent)
	}
	if data.MaxIter > 0 {
		cfg.MaxIterations = data.MaxIter
	}
	cfg.Source = "websocket"
	return cfg
}

func (h *AgentHandler) syncTurn(ctx context.Context, sessionID string, turn agent.UserTurnInput, data ChatData) (string, int, error) {
	if runtime, ok := h.agent.(loopConfigRuntime); ok {
		sess, _ := h.agent.Sessions().Get(sessionID)
		result, err := runtime.RunLoopWithSessionInput(ctx, sess, turn, h.chatLoopConfig(data))
		if err != nil {
			return "", 0, err
		}
		return result.Response, result.Iterations, nil
	}
	response, err := h.agent.ChatWithSessionInput(ctx, sessionID, turn)
	return response, 1, err
}
