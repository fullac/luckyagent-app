package server

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/yurika0211/luckyagent-runtime/provider"
	"github.com/yurika0211/luckyagent-runtime/session"
	"github.com/yurika0211/luckyagent-runtime/tool"
)

// handleSessionToolTrace returns an annotated trace reconstructed
// from the session's persisted provider messages.
func (s *Server) handleSessionToolTrace(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed, "")
		return
	}

	var templates map[string]string
	if s.agent != nil && s.agent.Config() != nil {
		templates = s.agent.Config().Get().ToolTrace.Templates
	}
	turn := r.URL.Query().Get("turn")
	if turn != "" && turn != "first" {
		s.sendError(w, "turn must be first or omitted", http.StatusBadRequest, "")
		return
	}
	messages := sess.GetMessages()
	if turn == "first" {
		// Legacy compaction can remove the original turn. Do not relabel a
		// follow-up as the delegated execution merely because it is now first.
		for _, message := range messages {
			if message.Role == "user" {
				break
			}
			if session.IsCompactBoundary(message) {
				s.sendError(w, "original execution turn is no longer available", http.StatusGone, "")
				return
			}
		}
		messages = firstSessionTurn(messages)
	}
	allRecords := sessionToolTraceRecords(messages, templates)
	successes := 0
	failures := 0
	pending := 0
	for _, record := range allRecords {
		if record.Status == "pending" {
			pending++
		} else if record.Success {
			successes++
		} else {
			failures++
		}
	}
	rate := 0.0
	if finished := successes + failures; finished > 0 {
		rate = float64(successes) / float64(finished)
	}
	limit := boundedQueryInt(r, "limit", defaultToolTraceLimit, 1, maxToolTraceLimit)
	offset := boundedQueryInt(r, "offset", 0, 0, 1_000_000)
	start, end := tracePageBounds(len(allRecords), limit, offset)
	records := truncateTraceRecords(allRecords[start:end])

	s.sendJSON(w, http.StatusOK, map[string]any{
		"session_id":    sess.ID,
		"turn":          turn,
		"tools":         records,
		"total_calls":   len(allRecords),
		"successes":     successes,
		"failures":      failures,
		"pending_calls": pending,
		"success_rate":  rate,
		"limit":         limit,
		"offset":        offset,
		"returned":      len(records),
		"has_more":      start > 0,
	})
}

// A delegated execution owns the first user turn of its execution session.
// Later follow-up conversations must not appear as work done by that task.
func firstSessionTurn(messages []provider.Message) []provider.Message {
	start := -1
	for i, message := range messages {
		if message.Role != "user" {
			continue
		}
		if start >= 0 {
			return messages[start:i]
		}
		start = i
	}
	if start < 0 {
		return nil
	}
	return messages[start:]
}

func tracePageBounds(total, limit, offset int) (int, int) {
	if total <= 0 || offset >= total {
		return total, total
	}
	end := total - offset
	start := end - limit
	if start < 0 {
		start = 0
	}
	return start, end
}

const (
	traceArgumentMaxChars   = 4096
	traceResultMaxChars     = 4096
	traceAnnotationMaxChars = 1024
	traceErrorMaxChars      = 1024
)

func truncateTraceRecords(records []tool.TraceRecord) []tool.TraceRecord {
	result := make([]tool.TraceRecord, len(records))
	for i, record := range records {
		record.Arguments = truncateTraceText(record.Arguments, traceArgumentMaxChars)
		record.Result = truncateTraceText(record.Result, traceResultMaxChars)
		record.Annotation = truncateTraceText(record.Annotation, traceAnnotationMaxChars)
		record.Error = truncateTraceText(record.Error, traceErrorMaxChars)
		result[i] = record
	}
	return result
}

func truncateTraceText(text string, maxChars int) string {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text
	}
	marker := "\n...[truncated]"
	keep := maxChars - utf8.RuneCountInString(marker)
	if keep < 0 {
		keep = 0
	}
	return string([]rune(text)[:keep]) + marker
}

func sessionToolTraceRecords(messages []provider.Message, templateSets ...map[string]string) []tool.TraceRecord {
	records := make([]tool.TraceRecord, 0)
	var templates map[string]string
	if len(templateSets) > 0 {
		templates = templateSets[0]
	}
	pendingByID := make(map[string]int)
	pendingByName := make(map[string][]int)

	for _, message := range messages {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				record := tool.NewTraceRecordWithTemplates(call.Name, call.Arguments, "", 0, templates)
				record.ID, record.Status, record.Success = call.ID, "pending", false
				records = append(records, record)
				index := len(records) - 1
				if call.ID != "" {
					pendingByID[call.ID] = index
				}
				name := strings.TrimSpace(call.Name)
				pendingByName[name] = append(pendingByName[name], index)
			}
			continue
		}
		if message.Role != "tool" {
			continue
		}

		index, found := pendingByID[message.ToolCallID]
		if !found {
			name := strings.TrimSpace(message.Name)
			for _, candidate := range pendingByName[name] {
				if records[candidate].Status == "pending" {
					index, found = candidate, true
					break
				}
			}
		}
		if !found {
			record := tool.NewTraceRecordWithTemplates(message.Name, "", message.Content, 0, templates)
			record.ID = message.ToolCallID
			records = append(records, record)
			continue
		}

		id := records[index].ID
		records[index] = tool.NewTraceRecordWithTemplates(records[index].Name, records[index].Arguments, message.Content, 0, templates)
		records[index].ID = id
	}
	return records
}
