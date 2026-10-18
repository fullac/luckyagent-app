package websocket

import (
	"encoding/json"
	"testing"
)

func TestTaskEventDataCarriesExecutionSessionLinks(t *testing.T) {
	data := TaskEventData{
		Type:               "task.progress",
		TaskID:             "task-1",
		ExecutionSessionID: "delegate-task-1",
		ParentSessionID:    "chat-1",
		Role:               "subagent",
		Metadata:           map[string]string{"agent_id": "coder"},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"execution_session_id": "delegate-task-1",
		"parent_session_id":    "chat-1",
		"role":                 "subagent",
	} {
		if got := decoded[key]; got != want {
			t.Fatalf("%s = %v, want %s; JSON=%s", key, got, want, raw)
		}
	}
	if _, ok := decoded["metadata"]; !ok {
		t.Fatalf("metadata missing from task event JSON: %s", raw)
	}
}
