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
- Long-task context uses `context.max_context_tokens` and the reply reserve
  consistently across planning, automatic compaction and durable continuation.
  Legacy JSON with only `max_tokens` migrates to that context limit; an explicit
  canonical value wins. The legacy setter updates both. `limits.max_tokens`
  continues to control generation output.
- The Agent preserves complete tool exchanges, bounds the model's summary
  projection, and exposes `context_history_read` for current-session evidence.
  Durable execution derives a task-state ledger from saved operations and
  reviews execution requests even when the first response only promises work.
  The context inspection bucket maps can now contain `project`; clients should
  accept this additive key. No HTTP route or session format changes are required.
- Persisted tool messages may include `run_id`, `started_at`, `duration_ms` and
  `status`. `GET` session tool trace uses `duration_ms` for the matched tool
  result. Older transcripts omit the field, and those records stay at duration 0.

`POST /api/v1/context/fit` retains its existing text DTO: `role`, `content`,
`priority` and `category`. Native `tool_calls` and `tool_call_id` are outside
that endpoint's current request/response contract. The Agent/runtime support
for complete tool exchanges does not add those fields to the HTTP adapter.

Transport routes, event fields and configuration defaults are preserved. The
configuration schema and request mapping live in `luckyagent-runtime`; capture
and tool schemas live in `luckyagent-tools`; recovery storage lives in
`luckyagent-api`. The launcher repository owns `config.example.json` and the
product field guide. Do not duplicate these implementations in the app.

Validate dependency upgrades with `GOWORK=off`, so local workspace replacements
cannot mask missing published module versions. Native desktop capture and
provider cache benefits require separate environment-specific measurements.
