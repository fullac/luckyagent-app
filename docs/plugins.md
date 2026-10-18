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
their full hook protocols, LSP or OAuth.

## HTTP plugin management

The Android client uses the following authenticated routes, registered by
`apiapp/plugin_management.go` under the same transport policy as other APIs:

| Method | Route | Operation |
| --- | --- | --- |
| GET | `/api/v1/plugins` | Verified installed inventory, including disabled and damaged entries |
| POST | `/api/v1/plugins` | Install `{source, ref?, subdir?, trust?}` |
| GET | `/api/v1/plugins/{name}` | Inspect one installed plugin |
| POST | `/api/v1/plugins/{name}/enable` | Save enabled state |
| POST | `/api/v1/plugins/{name}/disable` | Save disabled state |
| POST | `/api/v1/plugins/{name}/update` | Reinstall recorded source with `{trust:false}` by default |
| DELETE | `/api/v1/plugins/{name}` | Remove installation registration; retain immutable cache |

Inventory is `{plugins:[...], count:N, changes_require_restart:true}`. Entries
include name/version/description, source/ref/revision/subdir/digest/installed_at,
enabled/trusted/requires_trust, contribution names (`skills`, `commands`, `agents`,
`mcp_servers`), `hook_count`, and optional `error`. Prompt contents, hook commands,
MCP environment and credentials are omitted; source URL userinfo/query/fragment
are redacted. A missing installation directory returns an empty inventory without
creating directories. An unreadable installation index returns an error; individual
damaged bundles stay visible with their error so they can be removed or updated.

Mutation responses include `action`, `name`, optional `plugin`,
`runtime_restart_required:true` and `message`. Install returns 201; other successful
operations return 200. Missing plugins return 404; unsupported methods return 405;
invalid requests return 400, oversized JSON 413 and unsupported media types 415;
a busy manager or conflicting trust/digest returns 409.
Storage failures return 500 and operation deadlines return 504. Install JSON is
limited to 16 KiB, update JSON to 4 KiB; Git/copy operations have a 90 second deadline.

Local paths refer to the server filesystem. `trust` defaults to false on every
install/update, and an update retains a manually disabled state. Executable MCP
and hook plugins installed without trust remain disabled. Pure prompt plugins can
be enabled without execution trust. These endpoints never execute the installed
plugin or reload the Agent; restart each running API/gateway instance to apply
changes. No configuration keys were added.

Validation from this module, using published module pins or a local workspace:

```bash
go test -race ./cli/lhcmd ./apiapp -run TestPlugin
go test -race ./channelapp/telegram ./channelapp/qqofficial
```

The launcher repository maintains the full user guide in `docs/plugin-module.md`
and HTTP request/response examples in `docs/API.md`.
