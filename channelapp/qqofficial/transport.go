// Package qqofficial keeps source-compatible host bindings to the independent gateway module.
// Platform implementations live only in github.com/yurika0211/luckyagent-gateway.
package qqofficial

import transport "github.com/yurika0211/luckyagent-gateway/qqofficial"

type Adapter = transport.Adapter
type Config = transport.Config

var DefaultConfig = transport.DefaultConfig
var NewAdapter = transport.NewAdapter

type outboundMedia = transport.OutboundMedia

const outboundMediaDocument = transport.OutboundMediaDocument
const outboundMediaPhoto = transport.OutboundMediaPhoto

var qqMediaDeliveryGuidance = transport.QqMediaDeliveryGuidance

var resolveOutboundMediaResponse = transport.ResolveOutboundMediaResponse

var splitQQMessageChunks = transport.SplitQQMessageChunks
var qqRuneLen = transport.QqRuneLen

const qqProgressTraceChunkLimit = transport.QqProgressTraceChunkLimit
