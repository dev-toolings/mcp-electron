# Tools

All page tools take `session` (omit with one session) and `window` (index
from `windows`, default 0). Action tools take `snapshot: diff|full|none`
(default diff) and `timeout_ms` (target lookup, default 10000).

| Tool | Arguments | Notes |
|---|---|---|
| `launch` | `target` or `command`, `args`, `env`, `cwd`, `isolate` (true), `inspect` (true), `startup_timeout_ms`, `name` | Starts and attaches; returns session, windows, first snapshot |
| `attach` | `endpoint` (http://127.0.0.1:PORT or PORT), `inspector_endpoint`, `name` | Never owns the app; an endpoint a session already drives (any spelling: `9333`, `localhost:9333`) returns that session instead of opening a second one |
| `status` | | Sessions, state (running/restarting/exited), restarts, ports, windows |
| `windows` | `session` | Index, type (page/webview), title, URL |
| `logs` | `last` (100), `errors`, `contains`, `source`, `since_seconds`, `window` | Sources: console, exception, network, page, main, main-exception, stdout, stderr |
| `stop` | `session`, `keep_artifacts` | Terminates owned app and removes its profile; deletes the session's screenshots and flow outputs unless `keep_artifacts: true` |
| `snapshot` | `level` skeleton/content/full, `selector` | Header: title, URL, interactive count, errors since previous snapshot |
| `click` | `target`, `button`, `double`, `hover_first` | Per-row buttons shown only on `:hover`: omitted, a target hidden by `visibility`/`opacity` is revealed by moving the real mouse over it first; `true` also reveals a `display:none` target by hovering its row; `false` never hovers |
| `type` | `text`, `target`, `append`, `submit` | Without target: focused element |
| `fill_form` | `fields` (JSON object target → text, or a list of `{target, value}` filled in order) | |
| `select` | `target`, `values` | Labels or values |
| `press` | `key`, `target` | `Enter`, `Escape`, `cmd+k`, `ctrl+shift+p`, `shift+Tab`, single characters |
| `hover` | `target` | |
| `drag` | `from`, `to`, `steps` | |
| `scroll` | `target` (into view) or `dy` | |
| `upload` | `target`, `files` (absolute) | |
| `wait_for` | `target`, `state` visible/hidden/attached/enabled/stable, `text`, `url`, `load`, `timeout_ms` | No condition: plain delay. `hidden`: no element matching the target is visible (several matches: all must be hidden; an element scrolled out of view still counts as visible; a removed ref is hidden); `text` with `state: hidden` waits for the text to go away |
| `screenshot` | `target`, `full_page`, `format` webp/jpeg/png, `quality`, `save`, `name` | `save` writes a PNG in the session artifacts dir (deleted by `stop`) |
| `eval` | `expression`, `target`, `timeout_ms` (8000, max 120000) | Renderer; promises awaited. On timeout the error says whether the window still answers (slow promise) or its main thread is busy |
| `eval_main` | `expression`, `timeout_ms` (8000, max 120000) | Main process via Node inspector; Electron modules in scope. Returns the last expression's value like the DevTools console (expression, IIFE, async IIFE, statements, `return`, top-level `await`); promises awaited; strings as is, other values as JSON; errors reported |
| `navigate` | `url` or `history` back/forward, `wait` | |
| `reload` | `ignore_cache` | Renderer reload, returns snapshot |
| `dialog` | `action` accept/dismiss, `text` | JavaScript dialogs (alert/confirm/prompt) |
| `run_flow` | `flow` (YAML) or `file`, `env`, `timeout_ms` | See flows.md |

## Snapshot format

```
[window 0] "Escape" app://index.html | 14 interactive | 1 error(s) since last snapshot
main
 h1 Votre équipe commence ici.
 @1 b Créer un agent
 @2 t Nom de l’agent
 txt Choisissez vos projets
errors:
  16:42:01.120 w0 exception/error TypeError: x is undefined  (app://index.js:120)
tags: b button, a link, t textbox, c checkbox, r radio, s combobox, m menuitem, x tab, hN heading, txt text, li item, img image; ✓/✗ checked, off disabled
```

## Targets

- `@3` (also `3`, `e3`): ref from the last snapshot of that window. Stale
  refs are re-resolved by role and name when the DOM re-rendered; otherwise
  the error asks for a new snapshot.
- CSS selector: must match exactly one element (`[data-testid="save"]`).
- `getByRole("button", { name: "Save" })`, `getByText("Save")`,
  `getByLabel("Email")`: case-insensitive substring on the accessible name,
  must match one visible element. A regex works too:
  `getByRole("button", { name: /^Mode : / })`, `getByText(/^Row \d+$/)`,
  `getByLabel(/e-?mail/i)` (RE2 syntax for names: no lookaround), and
  `exact: true` asks for the whole name, case-sensitive. Shorthands:
  `text=Save`, `label=Email`, `role=button name=Save`,
  `role=button name=/^Save/i`, `role=textbox[name="Message"]`. Any other
  `getBy…` call is an error, and so is `>>` chaining (use one CSS selector or
  a `@ref`): both fail at once instead of waiting.

## Main process recipes (`eval_main`)

```js
BrowserWindow.getAllWindows().map(w => ({id: w.id, title: w.getTitle(), url: w.webContents.getURL(), bounds: w.getBounds(), focused: w.isFocused()}))
ipcMain.eventNames()
app.getPath('userData')
app.getVersion()
dialog.showOpenDialog = async () => ({canceled: false, filePaths: ['/tmp/fixture']}); 'stubbed'
(() => { dialog.showMessageBox = async () => ({response: 0}); return 'stubbed' })()
(async () => { const w = BrowserWindow.getAllWindows()[0]; return await w.webContents.executeJavaScript('document.title') })()
const wins = BrowserWindow.getAllWindows(); if (!wins.length) return 'none'; wins.map(w => w.getTitle())
BrowserWindow.getAllWindows()[0].webContents.send('channel', {payload: 1}); 'sent'
nativeTheme.themeSource = 'dark'; nativeTheme.shouldUseDarkColors
```

A packaged app whose `EnableNodeCliInspectArguments` fuse is off ignores
`--inspect`: `eval_main` then reports that the inspector is not connected.
