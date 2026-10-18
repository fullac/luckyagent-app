// Package server preserves host imports while implementation is assembled from independent edge modules.
package server

import app "github.com/yurika0211/luckyagent-app/apiapp"

type ChatRequest = app.ChatRequest
type ChatResponse = app.ChatResponse

const DefaultPairingTTL = app.DefaultPairingTTL

var DefaultServerConfig = app.DefaultServerConfig

type ErrorResponse = app.ErrorResponse

var LanAdvertiseURL = app.LanAdvertiseURL

type MemoryEntry = app.MemoryEntry

var New = app.New

type PairingKey = app.PairingKey
type PairingPayload = app.PairingPayload
type Server = app.Server
type ServerConfig = app.ServerConfig
type ServerStats = app.ServerStats
