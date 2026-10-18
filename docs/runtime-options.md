# Runtime request and recovery options

Application startup composes the public Agent with the independently versioned
runtime, tools and API modules. The pinned dependencies provide these options:

- WebSocket startup restores active run metadata without retaining every event
  history. Reconnect replay and subsequent run updates still load each session
  on demand. The host remains responsible for starting recovered runs.
- OpenAI Chat Completions and Responses accept an optional
  `models.endpoints.chat.prompt_cache_key`. It defaults to an empty value, which
  omits the wire field. Each `fallbacks[].prompt_cache_key` is independent.
  Configuration reload follows the existing provider rebuild path.
- X11 capture uses a bounded native local-display fallback when ImageMagick
  fails. Input/window selection still uses xdotool; WSLg has a separate backend.

Transport routes, event fields and configuration defaults are preserved. The
configuration schema and request mapping live in `luckyagent-runtime`; capture
and tool schemas live in `luckyagent-tools`; recovery storage lives in
`luckyagent-api`. The launcher repository owns `config.example.json` and the
product field guide. Do not duplicate these implementations in the app.

Validate dependency upgrades with `GOWORK=off`, so local workspace replacements
cannot mask missing published module versions. Native desktop capture and
provider cache benefits require separate environment-specific measurements.
