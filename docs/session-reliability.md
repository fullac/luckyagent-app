# Session history and deletion

Session storage belongs to `luckyagent-runtime/session`; this application uses
its error-returning reads and shared `appservice.Sessions.Delete` operation.
No route, request field, configuration key or session format is added.

## HTTP

`GET /api/v1/sessions/{id}` reports a history load failure as HTTP 500, both
with and without pagination. The `/tools` trace subresource uses the same
policy. Failed reads do not return a successful empty history or trace. A
later request can retry after the underlying storage is repaired.

Title persistence errors from `POST /api/v1/sessions` and metadata errors from
`PATCH /api/v1/sessions/{id}` are returned to the caller. They are not
acknowledged as successful writes. A history, trace or metadata operation
already holding a deleted session handle receives HTTP 410; a request for an
ID no longer registered in the session manager receives HTTP 404.

HTTP execution retains the handle fetched during request validation. SSE and
gRPC streaming pass that handle to the Agent's
`ChatWithSessionStreamForSession` entrypoint, so deleting and reusing an ID
between validation and execution cannot switch the request to a new session.

`DELETE /api/v1/sessions/{id}` first deletes the durable session through the
shared application service, which also forgets its short-term memory. Only
after deletion succeeds does the server clear queued WebSocket turns, cancel
its active socket/HTTP turns, and ask the Agent to cancel registered calls for
that ID. A failed deletion does not initiate cancellation.

The runtime tombstone prevents an existing handle from writing the deleted
session back while cancellation reaches the running work. Cancellation is
cooperative: providers and tools must observe their context, and the API does
not terminate another process or another Agent instance.

## WebSocket

Each newly accepted run binds to a session handle, including initially unknown
session IDs. Queued work retains that handle. Before execution, the handler
checks the original handle and does not call `Ensure` again to recreate a
deleted session or adopt a new session that happens to reuse the same ID.
Duplicate request IDs continue to replay the existing run.

History read failures return `type: "error"` with
`data.code: "SESSION_HISTORY_FAILED"`; a deleted handle uses
`data.code: "SESSION_DELETED"`. The affected run does not emit a successful
`stream_end`. Synchronous result metadata is also read from the bound handle,
and streamed completion checks that the handle remains live.

The default Agent receives the bound pointer for both synchronous and streamed
execution. A custom injected runtime can implement
`ChatWithSessionStreamForSession` to retain that same streaming guarantee.
Legacy runtimes exposing only methods by session ID retain their existing
compatibility path and must enforce handle identity themselves.

Session deletion and the separate durable run store are not one transaction
across a process crash. These changes do not make external tool side effects
transactional or promise cancellation of uncooperative external work.

## SDK

`GetSession` returns a wrapped history storage error instead of an empty
snapshot. `DeleteSession` requests cancellation of calls registered with its
underlying Agent after successful deletion. Existing method signatures remain
unchanged; callers should continue to handle the returned errors and cancel
their own contexts when abandoning a request.

## Validation

Regression tests use temporary directories and local model servers. They
cover unreadable histories and repair/retry, failed metadata writes, deleted
handles, HTTP/SSE/WebSocket/SDK cancellation, and queued turns that must not
recreate or write into a replacement session with the same ID.

```bash
go test -race -count=1 -p 2 ./apiapp ./channelapp/websocket ./sdk
go vet ./apiapp ./channelapp/websocket ./sdk
```

Use a temporary joint workspace while changing the runtime and Agent helpers.
Before publishing application dependency pins, also validate with
`GOWORK=off` against the actual published versions.
