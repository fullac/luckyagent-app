package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yurika0211/luckyagent-tools/cron"
)

func TestHandleCronListsJobs(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	engine := a.CronEngine()
	if engine == nil {
		t.Fatal("expected cron engine")
	}

	if err := engine.AddJob("job-a", "Alpha", "first", cron.DailySchedule{Hour: 9, Minute: 30}, func() error { return nil }); err != nil {
		t.Fatalf("add job-a: %v", err)
	}
	if err := engine.AddJob("job-b", "Beta", "second", cron.IntervalSchedule{Interval: time.Hour}, func() error { return nil }); err != nil {
		t.Fatalf("add job-b: %v", err)
	}
	if err := engine.PauseJob("job-b"); err != nil {
		t.Fatalf("pause job-b: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cron", nil)
	w := httptest.NewRecorder()
	s.handleCron(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}

	var response struct {
		Running bool `json:"running"`
		Count   int  `json:"count"`
		Jobs    []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Schedule string `json:"schedule"`
			Status   string `json:"status"`
		} `json:"jobs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Count != 2 || len(response.Jobs) != 2 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Jobs[0].ID != "job-a" || response.Jobs[1].ID != "job-b" {
		t.Fatalf("jobs not sorted by id: %+v", response.Jobs)
	}
	if response.Jobs[0].Schedule == "" || response.Jobs[1].Schedule == "" {
		t.Fatalf("expected schedule text: %+v", response.Jobs)
	}
	if response.Jobs[1].Status != "paused" {
		t.Fatalf("expected job-b paused, got %+v", response.Jobs[1])
	}

	methodNotAllowed := httptest.NewRecorder()
	s.handleCron(methodNotAllowed, httptest.NewRequest(http.MethodPut, "/api/v1/cron", nil))
	if methodNotAllowed.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status: got %d", methodNotAllowed.Code)
	}
}

func TestHandleCronCreatePauseDelete(t *testing.T) {
	a := createTestAgent(t)
	s := New(a, DefaultServerConfig())
	body := `{"name":"晨报","description":"汇总","schedule":"每天9点","mode":"agent","command":"汇总昨日","metadata":{"session_id":"sess-1","platform":"android"},"delete_after_run":true}`
	w := httptest.NewRecorder()
	s.handleCron(w, httptest.NewRequest(http.MethodPost, "/api/v1/cron", strings.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		ID             string `json:"id"`
		Schedule       string `json:"schedule"`
		Expression     string `json:"expression"`
		Status         string `json:"status"`
		DeleteAfterRun bool   `json:"delete_after_run"`
	}
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Schedule != "每天09:00" || created.Expression != "0 9 * * *" || !created.DeleteAfterRun || created.ID == "" {
		t.Fatalf("created=%+v", created)
	}

	w = httptest.NewRecorder()
	s.handleCronByID(w, httptest.NewRequest(http.MethodPatch, "/api/v1/cron/"+created.ID, strings.NewReader(`{"action":"pause"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", w.Code, w.Body.String())
	}
	var paused struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&paused); err != nil {
		t.Fatal(err)
	}
	if paused.Status != "paused" {
		t.Fatalf("pause status=%s", paused.Status)
	}

	w = httptest.NewRecorder()
	s.handleCronByID(w, httptest.NewRequest(http.MethodDelete, "/api/v1/cron/"+created.ID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleCron(w, httptest.NewRequest(http.MethodGet, "/api/v1/cron", nil))
	var listed struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if listed.Count != 0 {
		t.Fatalf("job remained: %s", w.Body.String())
	}
}
