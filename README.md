# mcp-electron

An MCP server dedicated to designing, driving and debugging **Electron**
applications from an agent (Claude Code, Codex, any MCP harness). It is the
Electron-only extraction of [ghostchrome](https://github.com/dev-toolings/ghostchrome)'s
renderer engine (accessibility snapshots with refs, strict locators, mutation
diffs, console/network observers) plus an Electron host layer: launching a
project or a `.app` in isolation, the project's own dev loop with automatic
re-attach after restarts, process-tree teardown, and a Node inspector client
for the main process.

```
  agent (MCP client)
     │ stdio
     ▼
  mcp-electron ── internal/mcp     24 tools (session, page, flows)
       │
       ├── internal/electron       launch / attach / dev loop / logs
       │      ├── CDP  ───────────►  renderer windows   (internal/engine)
       │      └── Node inspector ─►  main process       (eval_main)
       │
       └── internal/flow           Maestro-style YAML flows (run_flow, CLI)
```

## Install

As a Claude Code plugin (MCP server + skill, updated with the plugin):

```
/plugin install mcp-electron --marketplace dev-toolings/mcp-electron
```

On older Claude Code versions: `/plugin marketplace add dev-toolings/mcp-electron`,
then `/plugin install mcp-electron@dev-toolings`.

As a bare MCP server, like `@playwright/mcp` (any MCP host; prebuilt for macOS
arm64/x64 and Windows arm64/x64):

```bash
claude mcp add --scope user mcp-electron -- npx -y @dev-toolings/mcp-electron@latest
```

From source, with the CLI installer (also registers Codex and writes the skill
to `~/.claude/skills` and `~/.codex/skills`; do not combine it with the
plugin, or Claude Code gets the server and the skill twice):

```bash
go build -o bin/mcp-electron ./cmd/mcp-electron
./bin/mcp-electron install --dry-run   # prints every step, writes nothing
./bin/mcp-electron install             # copy to ~/.local/bin, register, install skills
./bin/mcp-electron install --uninstall # remove registrations, skills and the copy
```

`install` copies the binary to `~/.local/bin/mcp-electron` (`--dest DIR`, or
`--bin PATH` to register an executable in place), registers it with Claude
Code (`claude mcp add --scope user`) and with Codex when its CLI is on PATH
(`--no-codex` to skip), replacing an existing entry, and writes the skill to
`~/.claude/skills/mcp-electron` and `~/.codex/skills/mcp-electron`.
`--uninstall` reverses all of it and verifies with `claude mcp get` /
`codex mcp get` that the entry is gone. `--dry-run` applies to both and never
writes, copies, removes or registers anything.

Manual registration: run `mcp-electron mcp` over stdio. Flags: `--artifacts
DIR` for screenshots and flow outputs (default
`~/Library/Caches/mcp-electron/sessions`), `--max-sessions N`,
`--idle-timeout D` (see below). `stop` deletes the session's artifacts
(`keep_artifacts: true` keeps them), and a starting server removes session
directories left there for more than 24 hours by a server that died.

## Resource control

Every session is a whole Electron process tree (main, GPU, renderers,
helpers), so the server keeps them bounded:

- **Session cap**: 3 open sessions by default (`--max-sessions`); `launch`
  and `attach` refuse beyond it and name the sessions to stop.
- **Idle stop**: a launched app with no tool or flow access for 30 minutes
  (`--idle-timeout`, `0` disables) is stopped and the reason is logged.
  Attached apps are never stopped.
- **Teardown**: `stop`, the idle timer, and the server's own exit (stdin
  closed, SIGTERM) terminate the full process tree top-down (SIGTERM, then
  SIGKILL after 10 s), including helpers that re-parented themselves, and
  delete the private profile.
- **Measurement**: `status` reports each session's live process count,
  resident memory and CPU share (one `ps` scan, at most once per second) and
  warns above 2 GB.
- **Bounded records**: the descendant record forgets processes that exited.
- **Bounded buffers**: process output rings (3000 / 5000 lines, lines cut at
  a fixed width), main-process console, per-window observer history (1000
  events), per-window request table (2000 entries, oldest finished dropped),
  `logs` answers (2000 entries max).
- **Bounded captures**: full-page screenshots refuse above 32 Mpx; use the
  viewport, an element, or scroll in steps.
- **Light polling**: process tree rescanned every 5 s, debugging endpoint
  probed every 3 s while healthy (every second while restarting), waits poll
  the DOM at the engine's intervals and stop as soon as the calling tool's
  context is cancelled.
- **Tests**: the engine suite launches one headless Chrome for the whole
  package; the Escape end-to-end test is opt-in.

## Tools

| Group | Tools |
|---|---|
| Session | `launch`, `attach`, `status`, `windows`, `logs`, `stop` |
| Page | `snapshot`, `click`, `type`, `fill_form`, `select`, `press`, `hover`, `drag`, `scroll`, `upload`, `wait_for`, `screenshot`, `eval`, `eval_main`, `navigate`, `reload`, `dialog` |
| Flows | `run_flow` |

Full reference: [plugin/skills/mcp-electron/references/tools.md](plugin/skills/mcp-electron/references/tools.md).
Agent-facing procedure: [plugin/skills/mcp-electron/SKILL.md](plugin/skills/mcp-electron/SKILL.md).

### Targets

`@N` refs from the last `snapshot`, a unique CSS selector, or
`getByRole("button", { name: "Save" })`, `getByText("Save")`,
`getByLabel("Email")`. Names and texts may be regexes
(`getByRole("button", { name: /^Mode : / })`) or exact (`exact: true`).
`getByText` keeps the innermost matching element and prefers an exact match,
so a button wrapped in nested divs is still unique. Actions wait for the
target to be visible and enabled, then return a diff of the interactive
elements; `click` first hovers a per-row button that only shows on `:hover`
(`hover_first`). `wait_for` with `state: hidden` holds when no element
matching the target is visible.

## Launching

- **Project directory** (`package.json` + `node_modules/electron`): runs the
  local Electron binary with `.` as the app. The app gets a private
  `--user-data-dir` under `/tmp` and `--remote-debugging-port` /
  `--inspect` on free loopback ports.
- **`.app` bundle** (must embed `Electron Framework.framework`) or a bare
  executable.
- **Dev command** (`command: ["bun","run","dev"]`): the server exports
  `MCP_ELECTRON_CDP_PORT` and `MCP_ELECTRON_INSPECT_PORT`; the dev script
  must pass them to Electron, e.g.
  `electron --remote-debugging-port=$MCP_ELECTRON_CDP_PORT --inspect=127.0.0.1:$MCP_ELECTRON_INSPECT_PORT .`
  `${MCP_ELECTRON_CDP_PORT}` also expands inside `command` and `env`. When
  the dev loop restarts Electron, the session reports `restarting`, then
  re-attaches on its own; `status` shows the restart count.
- **Attach**: `attach` with `http://127.0.0.1:PORT` of an app already
  started with `--remote-debugging-port`. Attached apps are never killed.

Apps that relocate their own data directory need their variable in `env`
(Escape: `ESCAPE_TEST_DATA`).

## Flows

Maestro syntax, desktop subset: `tapOn`, `doubleTapOn`, `inputText`,
`eraseText`, `pressKey`, `assertVisible`, `assertNotVisible`, `scroll`,
`scrollUntilVisible`, `waitForAnimationToEnd`, `extendedWaitUntil`,
`takeScreenshot`, `evalJs`, `assertJs`, `evalMain`, `assertMain`,
`repeat`, `runFlow`. Selectors: text, `role` + `text`, `label`, `id`,
`css`, `ref`. See [plugin/skills/mcp-electron/references/flows.md](plugin/skills/mcp-electron/references/flows.md)
and [examples/flows/escape-create-agent.yaml](examples/flows/escape-create-agent.yaml).

CI:

```bash
mcp-electron run-flow examples/flows/escape-create-agent.yaml \
  --target ~/Documents/lab/sandbox/escape --env ESCAPE_TEST_DATA=$(mktemp -d /tmp/esc-XXXX)
```

## Development

```bash
go test -short ./...        # unit tests (no browser)
go test ./...               # engine tests launch a headless system Chrome
MCP_ELECTRON_E2E_TARGET=~/Documents/lab/sandbox/escape \
MCP_ELECTRON_E2E_ENV="ESCAPE_TEST_DATA=@tmp" \
MCP_ELECTRON_E2E_FLOW=$PWD/examples/flows/escape-create-agent.yaml \
go test -count=1 -v -run 'TestEndToEndElectron|TestE2E' ./internal/mcp/ ./internal/electron/ ./cmd/mcp-electron/
```

The end-to-end scenarios open the app on screen and cover: the tool loop and
a flow (`internal/mcp`), `attach` leaving the app alive on `stop`, a hard
crash of the main process (state `exited`, helpers and profile cleaned), a
dev loop that restarts Electron (state `restarting`, automatic re-attach,
restart counted), a second window driven by its own index, and the server
stopping its apps when the host closes stdin (`cmd/mcp-electron`). `@tmp`
in the env gives each scenario a fresh data directory.

Layout: `internal/engine` (renderer engine from ghostchrome, attach-only
`Browser`), `internal/electron` (sessions, process tree, inspector),
`internal/mcp` (tools), `internal/flow` (YAML flows), `cmd/mcp-electron`
(CLI), `plugin/` (Claude Code plugin: manifest, `.mcp.json`, and the skill,
also embedded in the binary by `plugin/skills`), `.claude-plugin/` (the
marketplace), `npm/mcp-electron` (npm launcher), `scripts/release.ts`.

## Release

The npm package `@dev-toolings/mcp-electron` is a small Node launcher whose
`optionalDependencies` are one package per platform
(`@dev-toolings/mcp-electron-{darwin,win32}-{arm64,x64}`), each holding the
stripped Go binary; the package manager installs only the matching one. The
plugin's `.mcp.json` pins the same version, so the skill and the server always
ship together.

```bash
bun scripts/release.ts version 0.2.0   # plugin.json, .mcp.json, npm package.json
git commit -am "release 0.2.0" && git tag v0.2.0
git push origin v0.2.0                 # release workflow: test, build, publish npm, GitHub release
git push origin main                   # only once the release is green: the marketplace serves main
```

Pushing `main` last matters: plugin users get the new `.mcp.json` from the
default branch, and it must not point at a version that npm does not have
yet. The workflow needs an `NPM_TOKEN` secret (granular token with publish
rights on `@dev-toolings`). It is idempotent: re-running a failed release
skips the packages already published (platform packages always go first) and
updates an existing GitHub release.

CI runs on a single `ubuntu-latest` job (no macOS or Windows runners): the
binaries are pure Go and cross-compile in parallel in a few seconds,
`setup-go` caches modules and `GOCACHE` (test results included), and a newer
push cancels the running check. The end-to-end suites, which need a display
and Electron, stay local.
