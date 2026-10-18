package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemoryRecallEndpointsRespectCanceledRequests(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	for _, path := range []string{"/api/v1/memory/recall?q=project", "/api/v1/memory/recall/trace?q=project"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			w := httptest.NewRecorder()
			if path == "/api/v1/memory/recall?q=project" {
				s.handleMemoryRecall(w, r)
			} else {
				s.handleMemoryRecallTrace(w, r)
			}
			if w.Code != http.StatusRequestTimeout {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
