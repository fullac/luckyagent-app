package appservice

import (
	"github.com/yurika0211/luckyagent-runtime/session"
	"testing"
)

func TestNilHostSessionManagerIsUnavailable(t *testing.T) {
	var missing *session.Manager
	service := NewSessions(missing, nil)
	if got := service.Find("x", MatchSessionTitle); got.Status != SessionNotFound {
		t.Fatal(got)
	}
	if service.Delete("x", "") == nil {
		t.Fatal("missing host store accepted deletion")
	}
}

func TestSessionMetadataRejectsInvalidTitleBeforeMutation(t *testing.T) {
	sess := &session.Session{Title: "original"}
	empty := "  "
	pinned := true
	if err := UpdateSessionMetadata(sess, SessionMetadata{Title: &empty, Pinned: &pinned}); err == nil {
		t.Fatal("empty title accepted")
	}
	if pinned, _ := sess.Organization(); pinned || sess.Title != "original" {
		t.Fatal("invalid patch mutated session")
	}
}
