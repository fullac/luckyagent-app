// Package telegram keeps source-compatible host bindings to the independent gateway module.
// Platform implementations live only in github.com/yurika0211/luckyagent-gateway.
package telegram

import transport "github.com/yurika0211/luckyagent-gateway/telegram"

type Adapter = transport.Adapter
type Config = transport.Config

var DefaultConfig = transport.DefaultConfig
var NewAdapter = transport.NewAdapter

var normalizeLocalMediaPath = transport.NormalizeLocalMediaPath

type outboundMedia = transport.OutboundMedia

const outboundMediaDocument = transport.OutboundMediaDocument
const outboundMediaPhoto = transport.OutboundMediaPhoto

var prepareOutboundMediaResponse = transport.PrepareOutboundMediaResponse
var resolveOutboundMediaResponse = transport.ResolveOutboundMediaResponse
var summarizeOutboundMedia = transport.SummarizeOutboundMedia
var telegramBotCommands = transport.TelegramBotCommands
var telegramCommandNames = transport.TelegramCommandNames
var telegramCommandSpecs = transport.TelegramCommandSpecs
var telegramConversationScope = transport.TelegramConversationScope
var telegramHelpMessage = transport.TelegramHelpMessage
var telegramMediaDeliveryGuidance = transport.TelegramMediaDeliveryGuidance

var telegramWelcomeMessage = transport.TelegramWelcomeMessage

var NewAdapterWithClient = transport.NewAdapterWithClient
