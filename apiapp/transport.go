package server

import (
	transport "github.com/yurika0211/luckyagent-api"
	"net/http"
)

type ServerConfig = transport.Config
type ErrorResponse = transport.ErrorResponse
type rateLimiter = transport.Limiter

var DefaultServerConfig = transport.DefaultConfig
var newRateLimiter = transport.NewLimiter
var requestRemoteIP = transport.RemoteIP
var publicServerURL = transport.PublicServerURL

func (s *Server) transportPolicy() *transport.Middleware {
	return transport.NewMiddleware(s.config, s.rateLimiter, s.pairingKeyValid, func() { s.statsMu.Lock(); s.stats.ErrorReqs++; s.statsMu.Unlock() })
}
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return s.transportPolicy().CORS(next)
}
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return s.transportPolicy().Authenticate(next)
}
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return s.transportPolicy().RateLimit(next)
}
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return s.transportPolicy().Log(next)
}
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return s.transportPolicy().Recover(next)
}
