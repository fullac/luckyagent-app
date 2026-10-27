package server

import (
	"context"
	"net/http"
	"strings"

	transport "github.com/yurika0211/luckyagent-api"
	"github.com/yurika0211/luckyagent-runtime/provider"
)

// completeRequest stays smaller than ChatRequest. A session id, attachment, or
// iteration count would make this look like another chat turn.
type completeRequest struct {
	Message string `json:"message"`
}

// handleChatComplete asks the current chat provider once.
// Provider.Chat is the plain completion path: it sends no tool schema and does
// not open a session. A tool call in the response is a failure, not work to run.
func (s *Server) handleChatComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
		return
	}
	var req completeRequest
	if err := transport.DecodeJSON(w, r, &req, transport.DefaultJSONBodyLimit); err != nil {
		transport.SendRequestError(w, err)
		return
	}
	text, err := s.completeOnce(r.Context(), req.Message)
	if err != nil {
		s.sendError(w, "completion failed", completionStatus(err), err.Error())
		return
	}
	s.sendJSON(w, http.StatusOK, map[string]string{"response": text})
}

func (s *Server) completeOnce(ctx context.Context, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", errCompletionMessageRequired
	}
	if s.agent == nil || s.agent.Provider() == nil {
		return "", errCompletionUnavailable
	}
	resp, err := s.agent.Provider().Chat(ctx, []provider.Message{{Role: "user", Content: message}})
	if err != nil {
		return "", err
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" || len(resp.ToolCalls) > 0 {
		return "", errCompletionEmpty
	}
	return strings.TrimSpace(resp.Content), nil
}

type completionError struct {
	message string
	status  int
}

func (e *completionError) Error() string { return e.message }

var (
	errCompletionMessageRequired = &completionError{"message is required", http.StatusBadRequest}
	errCompletionUnavailable     = &completionError{"agent runtime unavailable", http.StatusServiceUnavailable}
	errCompletionEmpty           = &completionError{"model returned no text", http.StatusBadGateway}
)

func completionStatus(err error) int {
	var typed *completionError
	if errorsAsCompletion(err, &typed) {
		return typed.status
	}
	return http.StatusInternalServerError
}

func errorsAsCompletion(err error, target **completionError) bool {
	typed, ok := err.(*completionError)
	if !ok {
		return false
	}
	*target = typed
	return true
}
