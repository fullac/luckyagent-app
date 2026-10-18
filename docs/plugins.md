# Plugin application integration

`cli/lhcmd/plugin_cmd.go` provides `lh plugin list/show/validate/search/install/
update/enable/disable/remove`. Management uses `luckyagent-plugins` directly;
it never constructs an Agent, executes plugin scripts or connects MCP.
State lives under `${runtimeHome}/plugins`. Running instances use a startup
snapshot, so management changes require restart.

```bash
lh plugin install https://github.com/yurika0211/luckyagent-plugins.git --ref v0.1.0 --subdir examples/starter
lh plugin list
lh plugin show starter --json
lh plugin search review --marketplace /path/to/marketplace.json
```

The public Agent loads installed commands, Skills, instruction presets, native
hooks and trusted MCP declarations through `NewWithDependencies`; the default
application `bootstrap.New` activates prepared services explicitly. Plugins do
not introduce configuration keys or change the loose Skills directory.

CLI REPL and Telegram/QQ command routing recognize `/plugin:command` before
rejecting unknown built-in commands. Arguments retain case and punctuation;
the Agent expands `$ARGUMENTS` as prompt text and preserves the turn's scope,
attachments, session, reply context and delivery instructions. Existing transport
modules require no plugin dependency.

`GET /api/v1/commands` includes loaded plugin commands with `group: "plugins"`.
`POST /api/v1/commands` accepts `command`, `args` and optional `session_id`;
the command may have a leading slash. It runs the Agent Loop with configured
limits and approvals, creates a session when omitted and returns 404 for an
unknown supplied session. Results include `command`, `ok`, `output` and
`session_id`; loop failures use HTTP 200 with `ok: false`. API authentication
is unchanged. For attachments and per-request chat options use the regular
chat endpoints with `/plugin:command arguments` as the message.

Execution trust (`--trust`) is required for MCP, hooks and skill scripts.
Updates do not inherit trust for changed content. The Agent enforces tool
approval, sandbox restrictions, parent limits and cancellation. This release
supports an explicit subset of Codex/Claude manifests; it does not implement
their full hook protocols, LSP, OAuth or a plugin management UI.

Validation from this module, using published module pins or a local workspace:

```bash
go test -race ./cli/lhcmd ./apiapp -run TestPlugin
go test -race ./channelapp/telegram ./channelapp/qqofficial
```

The launcher repository maintains the full user guide in `docs/plugin-module.md`
and HTTP request/response examples in `docs/API.md`.
