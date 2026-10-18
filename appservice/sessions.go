// Package appservice binds the independent application-service module to the
// host's session and configuration types. Application rules live in the module.
package appservice

import (
	service "github.com/yurika0211/luckyagent-runtime/appservice"
	"github.com/yurika0211/luckyagent-runtime/session"
)

type SessionStore = service.SessionStore[*session.Session]
type SessionMatchMode = service.SessionMatchMode
type SessionMatchStatus = service.SessionMatchStatus
type SessionMatch = service.SessionMatch[*session.Session]
type Sessions = service.Sessions[*session.Session]
type SessionMetadata = service.SessionMetadata

const (
	MatchSessionID        = service.MatchSessionID
	MatchSessionIDPrefix  = service.MatchSessionIDPrefix
	MatchSessionTitle     = service.MatchSessionTitle
	MatchSessionReference = service.MatchSessionReference
	SessionNotFound       = service.SessionNotFound
	SessionMatched        = service.SessionMatched
	SessionAmbiguous      = service.SessionAmbiguous
)

func NewSessions(store SessionStore, forget func(string)) *Sessions {
	return service.NewSessions(store, forget)
}
func RenameSession(sess *session.Session, title string) error {
	return service.RenameSession(sess, title)
}
func UpdateSessionMetadata(sess *session.Session, patch SessionMetadata) error {
	return service.UpdateSessionMetadata(sess, patch)
}
