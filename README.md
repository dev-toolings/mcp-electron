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

As a Claude Code plugin (MCP server + skill, with the binary for macOS
universal and Windows x64 bundled: nothing else to install):

```
/plugin install mcp-electron --marketplace dev-toolings/mcp-electron
```

On older Claude Code versions: `/plugin marketplace add dev-toolings/mcp-electron`,
then `/plugin install mcp-electron@dev-toolings`.

For other MCP hosts, download the archive of your platform from the
[GitHub releases](https://github.com/dev-toolings/mcp-electron/releases)
(checksums in `SHA256SUMS`) and register `mcp-electron mcp` over stdio, or
build from source:

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
- **Idle stop**: a `target` launch with no tool or flow access for 30 minutes
  (`--idle-timeout`, `0` disables) is stopped and the reason is logged.
  A dev `command` session (the developer's working app) and attached apps
  are never stopped for idleness.
- **Teardown**: `stop`, the idle timer, and the server's own exit (stdin
  closed, SIGTERM) terminate the full process tree top-down (SIGTERM, then
  SIGKILL after 10 s), including helpers that re-parented themselves, and
  delete the private profile.
- **Dev commands outlive the server**: a `command` session writes its output
  to files and a state file under `<cache>/mcp-electron/dev/`. When the
  server exits it releases the app instead of stopping it, and the next
  `launch` of the same command in the same directory adopts it (same
  process, same ports) instead of starting a second copy. Only `stop` ends
  it. A recycled pid is never adopted (pid, process group and command line
  must all match). Not on Windows.
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
marketplace), `npm/mcp-electron` (npm launcher, unpublished), `scripts/release.ts`.

## Release

The marketplace (`.claude-plugin/marketplace.json` on `main`) serves the
plugin from the `dist` branch, which the release workflow replaces on every
tag with a single commit: `plugin/` (manifest, `.mcp.json`, skill) plus
`bin/mcp-electron` (universal macOS binary, assembled by the script since
Linux has no `lipo`) and `bin/mcp-electron.exe` (Windows x64, which Windows on
arm64 runs too). `.mcp.json` starts `${CLAUDE_PLUGIN_ROOT}/bin/mcp-electron`
directly: no Node, no npx, no registry, and no secret beyond the workflow's
own `GITHUB_TOKEN`.

```bash
bun scripts/release.ts version 0.2.0   # plugin.json (+ the dormant npm package.json)
git commit -am "release 0.2.0" && git tag v0.2.0
git push origin main v0.2.0            # release workflow: test, build, GitHub release, dist branch
```

The `dist` branch is generated: never commit to it. It moves only once the
build and the GitHub release succeeded, and re-running a release replaces it
again. To try a plugin build locally: `bun scripts/release.ts build 0.2.0 &&
bun scripts/release.ts plugin 0.2.0`, then `claude --plugin-dir dist/plugin`.

An npm distribution (`npm/mcp-electron`: Node launcher plus one package per
platform) is ready but not published; `bun scripts/release.ts stage` and
`publish` (with `NPM_CONFIG_TOKEN`) would ship it.

CI runs on a single `ubuntu-latest` job (no macOS or Windows runners): the
binaries are pure Go and cross-compile in parallel in a few seconds,
`setup-go` caches modules and `GOCACHE` (test results included), and a newer
push cancels the running check. The end-to-end suites, which need a display
and Electron, stay local.
