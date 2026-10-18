// Package telegram preserves host imports while implementation is assembled from independent edge modules.
package telegram

import app "github.com/yurika0211/luckyagent-app/channelapp/telegram"

type Adapter = app.Adapter
type AgentConfigProvider = app.AgentConfigProvider
type AgentConfigSnapshot = app.AgentConfigSnapshot
type Config = app.Config
type ContextWindowSnapshot = app.ContextWindowSnapshot

var DefaultConfig = app.DefaultConfig

type Handler = app.Handler

var NewAdapter = app.NewAdapter
var NewAdapterWithClient = app.NewAdapterWithClient
var NewHandler = app.NewHandler
var NewHandlerWithRuntime = app.NewHandlerWithRuntime
var NewRuntime = app.NewRuntime

type Runtime = app.Runtime
type TelegramFormatter = app.TelegramFormatter
