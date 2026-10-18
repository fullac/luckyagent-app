# LuckyAgent application module

This module owns CLI, HTTP/channel product operations and application assembly.
The public Agent is a separate leaf module; runtime backends are independent.

- Inspect `git status --short` before editing; preserve unrelated files.
- Import the public `lucky-agent/agent` and `luckyagent-runtime` packages.
  Never import the former host's private `luckyagent/internal` packages.
- Default construction goes through `bootstrap.New`, which prepares resources,
  injects the Agent and explicitly starts application services.
- Keep decoding, permissions, platform formatting and session bindings here.
  Shared operations belong to runtime/appservice; transport engines remain in
  the existing API/gateway modules and must not depend on the Agent.
- Preserve HTTP/WS/gRPC fields, configuration keys and session formats.
- Run the matching integration packages. For cross-cutting changes use
  `GOWORK=off go test -mod=readonly -race -p 2 ./...` and `go vet ./...`.
- The main launcher retains config.example.json, deployment/docs and build
  entrypoints. Update those consumers when changing the product surface.
