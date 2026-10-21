# Cursor ACP protocol reference (spike)

Verified 2026-10-21 against `~/.local/bin/agent` 2026.10.01-e373342, launched as `agent acp` (cwd = session cwd).
Client: `/Users/sid/code/harness/spikes/acp/spike.py` (Python 3, stdlib only; `modeswitch.py` for the live-switch scenario).
Fixtures: `/Users/sid/code/harness/internal/agent/acp/testdata/*.jsonl`. Each line is `{"dir":"in"|"out"|"stderr","msg":<json>,"t":<unix>}` ("in" = agent to client).
Scenarios were run on `composer-2.5[fast=true]` (cheap). `gemini-3.7-flash[effort=high]` was accepted by `set_model` but its prompt returned only the text "Upgrade your plan to continue" (plan limit), so it is not a usable cheap default for this account.

Run: `python3 spike.py <scenario> --cwd <git repo> [--set-model ID] [--set-mode ID] [--perm allow|reject|always|cancelled|first] [--prompt TEXT]... [--cancel-after SECS] [--load SESSION_ID]`.

## Transport
NDJSON JSON-RPC 2.0 over stdio, one object per line, no Content-Length. Logs go to stderr (none seen). `protocolVersion` is the integer 1. Agent-originated request ids are numeric and start at 0 (they collide numerically with our own ids, so keep the two id spaces separate per direction). Agent notifications and requests can arrive before the response to our request (e.g. `session/load` replays history before its response).

## Auth
`agent status` printed `Logged in as sidgroks@gmail.com`; no login was attempted. The agent still requires an `authenticate` call: `{"methodId":"cursor_login"}` returns `{}` and succeeds with the existing login. Without prior login the docs say run `agent login` (or `--api-key` / `CURSOR_API_KEY`, `--auth-token`). We did not test the unauthenticated error.

## initialize
Request: `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true},"clientInfo":{...}}`.
Response:
```json
{"protocolVersion":1,
 "agentCapabilities":{"loadSession":true,
   "mcpCapabilities":{"http":true,"sse":true},
   "promptCapabilities":{"audio":false,"embeddedContext":false,"image":true},
   "sessionCapabilities":{"list":{}}},
 "authMethods":[{"id":"cursor_login","name":"Cursor Login","description":"..."}]}
```
No `agentInfo` returned. `session/list {cwd}` is supported and returns `{sessions:[{sessionId,cwd,title,updatedAt}]}`.

## session/new  `{cwd, mcpServers:[]}`
Result: `{sessionId (uuid), modes, models, configOptions}`.
- `modes`: `currentModeId:"agent"`; available `agent` (full tools), `plan` (read-only planning), `ask` (Q&A, no edits or commands).
- `models`: `currentModelId:"default[]"`, 46 `availableModels` `{modelId,name}`. Model ids are parameterised strings: `family[param=value,...]`, e.g. `default[]` (Auto), `composer-2.5[fast=true]`, `claude-sonnet-5[thinking=true,context=300k,effort=high]`, `gemini-3.7-flash[effort=high]`. The list is one entry per family at default parameters (not the flat `agent --list-models` names like `claude-sonnet-5-thinking-high`). `name` is the bare family name.
- `configOptions`: two `select` options `{id:"mode",category:"mode",currentValue,options:[{value,name,description}]}` and `{id:"model",category:"model",...}` mirroring the above.
- The user's `~/.cursor/acp-config.json` (`selectedModelVariantId`) / cli-config choose the default; our run started on `default[]`.

## session/prompt  `{sessionId, prompt:[{type:"text",text}]}`
Response `{stopReason}` seen: `end_turn`, `cancelled`. No usage/token data anywhere (no `usage_update`, no `_meta`); spec 08's `usage` event cannot be filled from ACP.
Image blocks are supported (`promptCapabilities.image`), embedded context and audio are not.

### session/update variants observed
All are `method:"session/update"`, `params:{sessionId, update:{sessionUpdate:<kind>,...}}`.
| sessionUpdate | Payload | Notes |
|---|---|---|
| `agent_message_chunk` | `content:{type:"text",text}` | Token-level deltas, no messageId. |
| `agent_thought_chunk` | `content:{type:"text",text}` | Thinking text; interleaves with message chunks. Present even on non-"thinking" models. |
| `user_message_chunk` | `content` | Only during `session/load` replay. |
| `tool_call` | `toolCallId,title,kind,status:"pending",rawInput,locations?` | `rawInput` is often `{}` at first and filled by later updates. |
| `tool_call_update` | `toolCallId`, any of `title,status,rawInput,rawOutput,locations,content` | Sparse patches. Lifecycle `pending -> in_progress -> completed`. Never saw `failed`. |
| `plan` | `entries:[{content,priority,status}]` | Emitted in plan mode just before `cursor/create_plan`. Replaces the whole list. |
| `current_mode_update` | `currentModeId` | Emitted after each `set_mode` / mode config change. |
| `session_info_update` | `title` | Auto-generated title, early in the first turn. |
| `available_commands_update` | `availableCommands:[{name,description}]` | Slash commands (`copy-request-id`, `multi-model-review`, ...). Emitted right after the first prompt/load. |

`toolCallId` is an opaque string, sometimes containing a newline (`call-<uuid>-0\nfc_...`), so treat it as an opaque key.

### Tool kinds
- Read: `kind:"read"`, title `Read <file>`, `rawInput:{path}`, `rawOutput:{content}`. The agent reads the file itself (no `fs/read_text_file`).
- Search: `kind:"search"`, titles `Find` / `grep`; `rawInput:{pattern,...}`, `rawOutput:{totalFiles|totalMatches,truncated}`.
- Edit: `kind:"edit"`, title `Edit File` then `Edit \`<abs path>\``. On completion `content:[{"type":"diff","path","oldText","newText"}]`.
  - Modify: `oldText`/`newText` are the full old and new file contents (`"hello world\n"` -> `"hello acp\n"`).
  - Create: BUG-LIKE quirk, `oldText:"-- /dev/null"`, `newText:"++ b/<abs path>\n<content>"` i.e. a mangled unified-diff header is embedded in the text. The adapter must special-case a new file (detect the `-- /dev/null` / `++ b/` prefix and strip it).
  - The edit is already applied to disk by the agent; the diff arrives with the completed update, not before. Only seen in agent mode, with no permission request, for paths inside cwd.
- Execute: `kind:"execute"`, title is the command in backticks, `rawInput:{command}`, `rawOutput:{exitCode,stdout,stderr}` on completion (no live output, no `content`).
- Other: todo list (`title:"Update TODOs: ..."`, `rawInput:{_toolName:"updateTodos",todos}`), plan (`rawInput:{_toolName:"createPlan",name,plan}`).

## Agent-to-client requests
| Method | Seen? |
|---|---|
| `fs/read_text_file`, `fs/write_text_file` | **Never**, even with both capabilities advertised. The agent reads and writes disk directly. |
| `terminal/create`, `output`, `wait_for_exit`, `kill`, `release` | **Never**, even with `terminal:true`. See Shell below. |
| `session/request_permission` | Yes, for non-allowlisted shell commands. |
| `cursor/create_plan` | Yes, plan mode. Blocking request. |
| `cursor/update_todos` | Yes, todo lists. Sent **with an id** (a request), although the docs call it a notification: reply `{}`. |
| `cursor/ask_question`, `cursor/task`, `cursor/generate_image` | Not triggered; per docs, `ask_question` is blocking, the others are notifications. Implement defensively: if `id` is present, reply. |

The spike's fs and terminal handlers are implemented but were never exercised. Unknown methods get JSON-RPC `-32601`; the agent tolerated that for `cursor/*`.

## session/request_permission
Request (agent id 0, 1, ...):
```json
{"sessionId":"...","toolCall":{"toolCallId":"tool_85e8...","title":"`touch permtest.txt && date`","kind":"execute","status":"pending",
  "content":[{"type":"content","content":{"type":"text","text":"Not in allowlist: date, touch"}}]},
 "options":[{"optionId":"allow-once","name":"Allow once","kind":"allow_once"},
            {"optionId":"allow-always","name":"Allow always","kind":"allow_always"},
            {"optionId":"reject-once","name":"Reject","kind":"reject_once"}]}
```
Notes: `toolCall` has no `rawInput` (the command is in `title`, and was already announced by the earlier `tool_call` with the same `toolCallId`); the `content` text lists the offending program names ("Not in allowlist: ..."). Three options, ids are fixed strings, always these three for shell.
Responses (`result`):
- Allow: `{"outcome":{"outcome":"selected","optionId":"allow-once"}}`. The command then runs and `tool_call_update` completed carries `rawOutput`.
- Reject: `{"outcome":{"outcome":"selected","optionId":"reject-once"}}`. The tool call goes to `status:"completed"` with **no rawOutput** (not `failed`); the model sees a rejection and the turn ends normally (`end_turn`). Detect rejection by tracking that you rejected that `toolCallId`.
- Cancelled outcome `{"outcome":{"outcome":"cancelled"}}` is treated like reject: the model **retried the same command three times**, each time asking again, then gave up. Prefer an explicit `reject-once`.
- Allow always: persists `Shell(<program>)` into `~/.cursor/cli-config.json` `permissions.allow` (observed `Shell(uname)` added; we restored the file afterwards). This is a global, user-level side effect: our UI should probably map "always" to a harness-side rule and send `allow-once`, or warn.
- Pre-allowed commands (`~/.cursor/cli-config.json` `permissions.allow`, e.g. `Shell(ls)`, `Shell(echo)`, `Shell(git log)`) never produce a request. In `plan` mode, a `git log ... ; ls` combination did still prompt for the non-allowlisted part.
- Unanswered requests block the tool (per docs).

## Shell commands: Cursor runs them itself
Decisive finding for the Agent mirror: with `clientCapabilities.terminal:true` Cursor **never called `terminal/create`** (nor any `terminal/*`). Shell commands execute inside the agent process (sandboxed per its own policy) and the client only receives:
1. `tool_call{kind:"execute",rawInput:{command}}` (pending),
2. `session/request_permission` if not allowlisted,
3. `tool_call_update in_progress`,
4. `tool_call_update completed {rawOutput:{exitCode,stdout,stderr}}`.
Output is delivered **only at completion**, as a single combined `stdout`/`stderr` blob; no streaming, no PTY, no cwd or env echo. The same applies to file reads and edits (`fs/*` unused). We did not test whether omitting the `terminal` capability changes this (it did not matter when present). Docs (cursor.com/docs/cli/acp) also do not list `terminal/*` or `fs/*` as used by Cursor.

## session/cancel
Notification `{"method":"session/cancel","params":{"sessionId"}}` (no id). The in-flight `session/prompt` resolves with `stopReason:"cancelled"`. Tested with a `sleep 40` command running (permission already granted): the prompt returned ~within the cancel latency and `pgrep` found no leftover `sleep`, so the child process was killed. Pending permission requests are not separately cancelled by us; reply to them anyway.

## Live model and mode switching
- `session/set_model {sessionId,modelId}` -> `{}`. Works mid-session between turns (switched composer -> gemini after a completed turn; next turn ran). `modelId` must be one of the `availableModels` strings exactly, else `-32602 Invalid params`, `data.message:"Invalid model value: nonexistent-model"`. Emits no notification. Not tested during an in-flight turn.
- `session/set_mode {sessionId,modeId}` -> `{}`; also emits `current_mode_update`. Invalid mode -> `-32603` with `data.details:"Invalid mode ID: bogus. Valid modes: agent, plan, ask"`.
- `session/set_config_option {sessionId,configId:"mode"|"model",value}` -> `{configOptions:[...full list...]}` (no `current_mode_update` observed for this path).
Model parameters (thinking/effort/fast/context) are baked into the `modelId` string; switching effort means sending a different bracket string, which is only valid if that exact variant is advertised or parseable (we only used advertised ones).

## session/load  `{sessionId,cwd,mcpServers:[]}` in a new process
Requires `initialize` + `authenticate` first. Sessions persist under `~/.cursor/acp-sessions/<id>`. History is replayed as `session/update` notifications (`user_message_chunk`, `agent_thought_chunk`, `agent_message_chunk`; tool calls would presumably appear as `tool_call`) **before** the `session/load` response; the response is `{modes, models, configOptions}` with `currentModelId` restored to the last model set (composer), mode `agent`. Replayed turns that produced no output (the plan-limited gemini turn) are absent. The model recalled earlier turns correctly in a follow-up prompt. No `sessionId` in the response (use the one passed).

## MCP
`mcpServers:[]` accepted. Docs: ACP reads project/user `.cursor/mcp.json`; team servers unsupported; capability `mcpCapabilities {http,sse}` advertised for client-provided servers (not tested).

## Spec 14 corrections
1. **`terminal/*` is not used by Cursor.** Spec 14 ("`terminal/*` runs commands in a PTY owned by the host") and spec 06's claim "Cursor ACP sends commands through the client's `terminal/*` methods ... stream live" are wrong for agent 2026.10.01. Cursor runs shell itself and reports output only on completion (`rawOutput.stdout/stderr/exitCode`). The Agent mirror for Cursor must behave like Claude: show `$ cmd` on `tool_call`, output + exit status on completed `tool_call_update`, spinner meanwhile. Keep `terminal/*` as optional/defensive client methods, but do not design around them. Consequently the "survives app quit / allowlist via our PTY" story does not apply: execution and sandboxing are the agent's, and the allowlist is Cursor's (`~/.cursor/cli-config.json`).
2. **`fs/*` is also never sent.** "Go straight to disk, scoped to worktree" is dead code for now; scope enforcement cannot rely on it. The agent edits disk directly; the client learns of edits only from `tool_call_update` diff content. Implement `fs/*` defensively but advertising the capability gains nothing (consider not advertising, which could also avoid future surprises).
3. **Handshake needs `authenticate`** (`methodId:"cursor_login"`) even when logged in; spec says "if the agent requires it" - it always lists `authMethods`, and it must be called before `session/new`.
4. **Permission schema**: options are `allow-once` / `allow-always` / `reject-once` (kinds `allow_once`, `allow_always`, `reject_once`); no `reject_always`. Request has no `rawInput`, only `title` + a "Not in allowlist: ..." text. Answer `{outcome:{outcome:"selected",optionId}}`. `allow-always` mutates the user's global Cursor config; `cancelled` triggers retries. Rejected tool calls end `completed` with no output.
5. **Models**: from `session/new.models.availableModels` (46 entries, parameterised ids), not a static list; `agent --list-models` uses different flat ids and must not be mixed. **Live switching works**: `session/set_model` (and `set_mode`, `set_config_option`) mid-session between turns.
6. **Extension methods missing from spec**: `cursor/create_plan` (blocking; reply `{outcome:{outcome:"accepted"|"rejected"|"cancelled"}}`; params include `plan` markdown, `name`, `overview`, `todos`), `cursor/update_todos` (arrives with an id; reply `{}`; `todos:[{id,content,status}]`, `merge:true` means patch by id), `cursor/ask_question` (blocking), `cursor/task`, `cursor/generate_image`. Map `update_todos` -> `todo_update`, `create_plan` + `plan` update -> `plan_proposal`. Plan mode flow: `plan` update, `cursor/create_plan`, tool_call_update completed.
7. **No usage/cost** data in ACP, so `usage{in,out,cache,costUsd}` is unavailable for Cursor. `run_result.providerSessionId` = the ACP `sessionId`.
8. **Diff content quirks**: modify diffs carry full file text; new-file diffs embed `-- /dev/null` / `++ b/<path>` markers inside `oldText`/`newText` and must be normalised. Edits are already applied when the diff arrives (no pre-apply approval in agent mode inside cwd).
9. **`session/load` replays history before responding** and restores model; `session/list {cwd}` is available for resume pickers.
10. **Modes**: ids `agent`/`plan`/`ask` confirmed; set via `session/set_mode` (emits `current_mode_update`).
11. Interrupt = `session/cancel` notification; prompt returns `stopReason:"cancelled"` and child processes are killed.
12. Not verified: client-supplied MCP servers, image prompts, `cursor/ask_question`, behaviour when not logged in, set_model during an in-flight turn, whether `terminal` capability omitted changes anything.
