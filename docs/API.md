# Harness Go↔frontend API contract

The frontend only renders. Go owns all state and logic: it pushes view-models over the bus, and the frontend sends intents back through bindings.

## Conventions
- **Bindings** are generated into `frontend/src/lib/bindings/github.com/sid/harness/internal/app/<service>.ts`. Import them with relative paths (there is no `@` alias), e.g. `import { Status } from "../../lib/bindings/github.com/sid/harness/internal/app/gitservice"`. Regenerate with `wails3 generate bindings -clean=true -ts -i -d frontend/src/lib/bindings`, which wipes that dir.
- **Errors:** calls marked `*` can reject. The rejection's `cause` has the shape `{code, message, files?, etag?}`, where `code` is one of `dirty | conflict | not_found | archived | not_confirmed | stale_hunk | git | bad_arg | internal`.
- **Scope keys:** `session:<id>` or `main:<projectId>`. Get one from `SessionService.Scope(id)` or `MainScope(projectId)`, which return `{key, projectId, kind, sessionId, root}`.
- **Bus:** `frontend/src/lib/bus.ts` exports `bus`. Server-opened channels are received with `bus.onOpen(prefix, ch => ch.onJSON(...))`. Client channels open with `bus.open(name)`. **Server-opened channels have no replay**: after attaching, call the matching getter binding to get the initial state.

## Bindings
- **ProjectService:**
  - Projects: `ListProjects`*, `AddProject(path)`*, `RemoveProject(id)`*, `TouchProject(id)`*, `SetSetupCmds(id, cmds[])`*.
  - Picker: `ListDir(path, dirsOnly, hidden)`*, `RepoBadges(paths)` (200ms budget) returns `{path: {branch, worktrees}}`, `Home()`.
  - Fuzzy matching: `FilterEntries(query, names)` and `Fuzzy(query, candidates, limit)`, both returning `[{index, score, positions}]`.
- **SessionService:**
  - Lifecycle: `ListSessions(projectId, includeArchived)`*, `CreateSession(projectId, {title, provider, model, mode, effort, scopeKind, baseRef, forkedFrom})`*, `GetSession(id)`*, `RenameSession`*.
  - Cleanup: `ArchiveSession(id, force)`*, which returns `dirty` with the files when the worktree has changes. `RestoreSession`*, `DeleteSession(id, confirm)`*.
  - PRs: `RaisePR(id, title, body)`* returns the URL. `RefreshPR`*.
  - Scopes: `Scope(id)`*, `MainScope(projectId)`*.
  - **Agent runs:**
    - Messaging: `Send(sessionId, {text, attachments:[{name, mediaType, data(base64)}], delivery:"send"|"queue"|"steer"})`*. The first Send launches the host. A follow-up goes to the live host, or relaunches with the provider session id when the host is gone. While the run is busy, `queue` is held in Go and sent after the `run_result`. `steer` and `send` go straight to the CLI, except for Cursor, which holds everything. `Respond(sessionId, reqId, {behavior:"allow"|"allow_always"|"deny", message?})`*. `Interrupt(sessionId)`* also drops held messages.
    - Model: `SetModel(sessionId, modelKey "provider/id")`* and `CycleModel(sessionId, +1|-1)`* return `{forkedSessionId?}`. Within a provider the switch is live and persisted. Across providers it forks a session in the same worktree with `forkedFrom` set and a handoff as its first message. The source session goes idle, and the frontend should open the fork.
    - Mode and effort: `SetMode(sessionId, mode)`* takes the native or display name. `CycleMode(sessionId)`*: cursor goes agent→plan→ask, claude goes default→acceptEdits→plan. `SetEffort(sessionId, effort)`* applies at the next launch.
    - Reads: `Transcript(sessionId)`* returns `Block[]` and opens `blocks/<id>`. `ListModels()` returns `[{key, provider, id, label, favourite}]`, with favourites from settings.json `models.favourites` (key list).
    - `CreateSession` also runs the project's setup commands in a "setup" terminal tab of the session scope.
- **FSService:**
  - Files: `Read(path)`* returns `{content, etag, language, binary}`. `Write(path, content, ifEtag)`* returns the new etag, or `conflict` with `etag`.
  - File operations: `Create(path, isDir)`*, `Rename(from, to)`*, `Delete(path)`* (to the Trash).
  - Watching: `WatchScope(scopeKey, root)`*, `UnwatchScope(scopeKey)`.
- **SearchService:** `QuickOpen(root, query, limit)`* returns `[{path, score, positions}]`, with root-relative paths. `Grep(root, query, {regex, caseSensitive, glob})`* returns a `streamID`. `CancelGrep(streamID)`.
- **GitService:**
  - `Status(root)`* returns `SCMState`. `Refresh(scopeKey)`.
  - Hunks: `Hunks(root, path, staged)`*, `StageHunk/UnstageHunk/RevertHunk(root, path, hunkId)`*. **Refetch hunks after every hunk operation**, because the ids shift.
  - Files: `StageFiles/UnstageFiles/DiscardFiles(root, paths[])`*, `Commit(root, msg, amend)`*, `ShowFile(root, rev "HEAD"|":0", path)`*.
- **TermService:**
  - PTYs:
    - `Spawn(scopeKey, cwd, kind, cols, rows)`* returns a ptyId.
    - `Kill`*, `Resize(ptyId, cols, rows)`*.
    - `List(scopeKey)`, `ShellsRunning()`, `KillScope`.
  - Layout:
    - Reading and restoring: `Layout(scopeKey)`, `RestoreLayout(scopeKey, cwd)`*.
    - Creating panes: `NewTab(scopeKey, cwd)`* and `Split(scopeKey, paneId, "row"|"col", cwd)`*, both returning `{tabId, paneId, ptyId}`.
    - Intents:
      - Panes: `ClosePane(scopeKey, paneId)`*, `FocusPane(scopeKey, paneId)`, `FocusDir(scopeKey, "left"|"right"|"up"|"down")`, `ResizeDir(scopeKey, dir)`, `SetRatios(scopeKey, tabId, path[], ratios[])`, `ToggleMaximize(scopeKey)`.
      - Tabs: `NextTab`, `PrevTab`, `ActivateTab(scopeKey, tabId)`, `RenameTab(scopeKey, tabId, title)`, `MoveTab(scopeKey, from, to)`.
    - Ignore `Flush`.
- **LSPService:** `Acquire(scopeRoot, monacoLanguageId)`* returns the key, then open bus channel `lsp/<key>`. It fails with `not_found`: the message is the install hint when the server isn't installed, or "no language server for X".
- **UIStateService:** `Get(key)` returns the JSON string, or `""`. `Set(key, json)`*. Never write `term:*` keys.
- **SettingsService:**
  - `GetSettings()` returns `{values, error, path}`.
  - `GetKeybindings()` returns `{bindings:[{key, command, when, args, remove}], errors[], conflicts[], path}`, with keys in lowercase VS Code format (`cmd+shift+p`).
  - `OpenSettingsFile("settings"|"keybindings")`* returns the path.
- **AppService:** `ConfirmQuit()`.

## Bus channels
- **`fs/<scopeKey>` (server):** `{paths:[abs...]}`.
- **`scm/<scopeKey>` (server):** `SCMState`, shaped like this:
  ```
  {root, branch, upstream, ahead, behind, count, dirty,
   groups:[{id:"merge"|"staged"|"changes", label, count,
            rows:[{path, origPath?, name, dir, code, badge, class, title}]}]}
  ```
  - `class` is one of `scm-modified | scm-added | scm-deleted | scm-renamed | scm-untracked | scm-conflict`.
  - The state is pushed on WatchScope, 150ms after fs changes, and after every git mutation.
- **`termlayout/<scopeKey>` (server):** `{tabs:[{id, title, focused, maximized, root: Node}], active, seq}`.
  - `Node` is either a leaf `{type:"leaf", paneId, ptyId, kind}` or a split `{type:"split", dir:"row"|"col", ratios[], children[]}`.
  - In a `row` split the children run left to right; in a `col` split they run top to bottom.
- **`pty/<ptyId>` (client opens):**
  - Binary data goes both ways.
  - Send JSON `{type:"resize", cols, rows}` to resize.
  - The server sends JSON `{type:"exit", code}` when the shell exits.
  - On attach, the server replays a 1MB ring buffer.
- **`search/<streamID>` (server):**
  - Send any JSON frame (e.g. `{}`) after attaching to start the stream.
  - Then it sends one frame per file: `{path, matches:[{line, column, text, submatches:[{start, end}]}]}`.
  - It ends with `{done:true}`, or `{error}`.
- **`lsp/<key>` (client opens):**
  - One JSON-RPC message per frame, with no Content-Length header.
  - Go answers `initialize` from its cache and swallows `initialized`, `shutdown` and `exit`.
  - Any request id is safe.
  - A crash sends `{type:"lsp_status", state:"crashed"}` and closes the channel, after which you call Acquire again.
- **`blocks/<sessionId>` (server):**
  - It sends transcript patches: `{op:"snapshot", blocks:[...]}`, `{op:"append", block, afterId?}` and `{op:"update", block}`.
  - For text streaming, an update can carry only `{id, textDelta}`, which is appended to the block's text.
  - Text deltas are coalesced into frames of at most 16ms; each other patch is sent immediately, one patch per frame.
  - Get the initial state from `Transcript(id)`.
- **`sessions` (server):** the full list of rows for all projects (archived included), pushed (debounced 30ms) on every status change and session mutation. Each row is `{id, projectId, title, provider, model, mode, effort, status:"running"|"needs_approval"|"idle"|"error"|"archived", branch, dirty (changed file count), prUrl, prState}`. Status rules: `needs_approval` when a permission or plan request is pending, else `running` from a user message until the `run_result`, else `error` after an error run_result, a fatal error, a launch failure or a host crash (cleared by the next message), else `idle`.
- **`settings`, `keybindings` (server):** the same shapes as their getters.
- **`app` (server):** `{type:"quit-request", shells:N}`. Show the quit sheet, then call `AppService.ConfirmQuit()`.
- **`lsp` (server):** `[{key, id, root, state, refcount, pid}]`.

## Frontend module ownership (for parallel work)
- **Shell, layout, keys, palette and picker:** `App.tsx`, `state/*` (except `state/editor.ts`), `lib/commands.ts`, `lib/keys*.ts`, and `features/{agents,palette,projectpicker,shell}`.
  - **Toasts:** `import { toast, toastError, errInfo } from "../state/toast"`. `toast(msg, "info"|"success"|"error")`; `errInfo(e)` decodes a rejected binding's `{code, message, files?, etag?}`.
  - **Context keys:** put `data-ctx="terminal"|"editor"|"composer"` on your feature root; the shell sets `terminalFocus`/`editorFocus`/`composerFocus`. See the header of `lib/commands.ts`.
- **Terminal:** `features/terminal/*`, exporting `TerminalPanel({scopeKey, root})`.
- **Editor, explorer and LSP bridge:** `features/{editor,explorer}/*` and `lib/lsp-bridge/*`, exporting `EditorTab({scopeKey, root, path})`, `Explorer({scopeKey, root})` and `openFile(scopeKey, path)` from `state/editor.ts` (owned by the editor agent).
- **SCM and diff:** `features/{scm,diff}/*`, exporting `ScmPanel({scopeKey, root})` and `DiffTab({scopeKey, root, path, staged})`.
- **Chat and composer:** `features/{chat,composer,review}/*`, exporting `ChatTab({sessionId, scopeKey})`.
- **Commands:** every feature registers its commands and shortcuts through `lib/commands.ts`, using the fixed API in that file.
