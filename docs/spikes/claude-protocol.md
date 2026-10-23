# Claude Code stream-json protocol (spike)

Verified against `claude` 2.1.306, `--model haiku`, macOS, 2026-10-21. Every claim below was observed on the wire unless marked (bundle) = read from the CLI binary strings. Fixtures: `internal/agent/claude/testdata/<scenario>.jsonl` (raw stdout) and `<scenario>.stdin.jsonl` (what we sent). Drivers: `spikes/claude/*.py` (`drive.py` is the shared helper; run `python3 spikes/claude/s_<x>.py`).

Command: `claude --print --output-format stream-json --input-format stream-json --verbose --include-partial-messages --replay-user-messages --model haiku [--permission-mode M] [--permission-prompt-tool stdio] [--resume SID]`. One JSON object per line both ways.

## 1. Lifecycle

- **Process stays alive after `result`** waiting for the next stdin line (checked at +5s; follow-up turn works). It exits 0 only when stdin is closed. The host's idle-TTL design (spec 10) is correct as written.
- **`system/init` is lazy**: it is emitted when the first user turn starts, not at spawn. Control requests sent before any user message are answered normally. `init` is emitted again at the start of every turn (not just the first); the second one may list more tools (MCP servers finish connecting: `mcp_servers[].status` goes `pending` -> `connected`, and `mcp__*` tools appear). Treat each init as an update, do not assume one per process.
- **No `initialize` control request is required.** Everything below worked without it. (`initialize` exists and returns commands, agents, models, account, pid, current_permission_mode; optional.)
- stdin close mid-run: not tested beyond idle (exit 0).
- Bogus `--resume`: see section 7.

## 2. Stdin messages (client -> CLI)

User turn (works exactly as in the brief; `content` may also be a block array):
```json
{"type":"user","message":{"role":"user","content":"hello"}}
```
Control request:
```json
{"type":"control_request","request_id":"<any unique string>","request":{"subtype":"...", ...}}
```
Control response (to a CLI-initiated `can_use_tool`):
```json
{"type":"control_response","response":{"subtype":"success","request_id":"<echo of the control_request.request_id>","response":{ ...body... }}}
```

## 3. Stdout message types (CLI -> client)

Common fields on most: `session_id`, `uuid`, `parent_tool_use_id` (null unless from a subagent).

| type / subtype | Notes |
|---|---|
| `system/init` | `session_id`, `cwd`, `model` (resolved, e.g. `claude-haiku-5-5`), `permissionMode`, `tools[]` (names), `mcp_servers[{name,status,source}]`, `slash_commands`, `skills`, `agents`, `plugins`, `claude_code_version`, `apiKeySource`, `capabilities[]`, `output_style`, `memory_paths`, `fast_mode_state`. |
| `system/status` | Two shapes: `{status:"requesting"}` when an API request starts; `{status:null, permissionMode:"acceptEdits"}` when the permission mode changes (after `set_permission_mode`, and after ExitPlanMode approval -> `default`). |
| `system/thinking_tokens` | `{estimated_tokens, estimated_tokens_delta}` progress counter while thinking. |
| `system/task_started`, `system/task_notification` | Emitted around a Bash tool run: `{task_id, run_id, tool_use_id, description, task_type:"local_bash", is_backgrounded}` and `{..., status:"completed", output_file:"", summary}`. No output text in them. |
| `user` (replay) | Echo of our own turn because of `--replay-user-messages`: `message.content` is the string we sent, plus `isReplay:true`, `uuid`, `timestamp`. Distinguish from tool results by `isReplay` / string content. |
| `user` (tool_result) | `message.content:[{type:"tool_result",tool_use_id,content,is_error?}]` (`content` is a string, or an array of blocks e.g. `tool_reference`). Extra top-level fields: `tool_use_result` (rich, tool-specific: Read -> `{type:"text",file:{filePath,content,numLines,...}}`; Write -> `{type:"create",filePath,content,structuredPatch,originalFile}`; Bash -> `{stdout,stderr,interrupted,...}`; on deny it is the string `"Error: <message>"`), and `tool_result_meta:[{id,permission_decision:{decision:"accept"|"reject",source},non_execution_kind?}]`. `tool_use_result.structuredPatch` means diffs for Write/Edit can come from the CLI instead of being recomputed. |
| `stream_event` | `{event: <raw Anthropic SSE event>}`: `message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, `message_stop`. Delta types seen: `text_delta{text}`, `thinking_delta{thinking, estimated_tokens}`, `signature_delta`, `input_json_delta{partial_json}`. |
| `assistant` | **One message per content block**, not per API message: the same `message.id` is repeated, each carrying a single block (`thinking`, `text`, or `tool_use`). `message.stop_reason` is null here; the real stop reason is on the `message_delta` stream event. `tool_use` blocks carry the complete `input` (the `content_block_start` has `input:{}` and the JSON arrives via `input_json_delta`). Emitted after the block's deltas, before `content_block_stop`. |
| `rate_limit_event` | `{rate_limit_info:{status,resetsAt,rateLimitType,unifiedWindows...}}`. Ignore or surface. |
| `control_request` | CLI -> us: `can_use_tool` (below). |
| `control_cancel_request` | `{request_id}`: the CLI withdrew a pending `can_use_tool` (seen after `interrupt`). Drop the pending permission. |
| `control_response` | Reply to a control request we sent. |
| `result` | End of turn. See below. |

### thinking
`thinking_delta.thinking` and the `thinking` block text are **empty strings** for haiku here (only a signature and `estimated_tokens` are sent). Do not rely on thinking text; use `thinking_tokens` for a spinner.

### result
Key order puts `type` late; parse by key. Fields: `type:"result"`, `subtype` (`success` | `error_during_execution` | ...), `is_error`, `result` (final assistant text, only on success), `session_id`, `num_turns`, `stop_reason`, `terminal_reason` (`completed`, `aborted_streaming`), `duration_ms`, `duration_api_ms`, `total_cost_usd`, `usage{input_tokens,output_tokens,cache_read_input_tokens,cache_creation_input_tokens,iterations[]...}`, `modelUsage{<model>:{inputTokens,outputTokens,cacheReadInputTokens,cacheCreationInputTokens,costUSD,contextWindow,maxOutputTokens}}`, `permission_denials[]`, `errors[]` (on error subtypes), `uuid`. Measured over two turns (multiturn fixture): `usage` is **per-turn** (turn 2: output_tokens 4), while `total_cost_usd` (0.00264 -> 0.00288) and `modelUsage` (outputTokens 238 -> 242) are **cumulative for the process**. Per-turn cost = difference of consecutive `total_cost_usd`. (Not determined whether a resumed process restarts the cumulative count; diff only within one process.)

## 4. Permissions: `--permission-prompt-tool stdio` WORKS

Works with `--permission-mode default` and `plan`. Read-only tools (Read, ToolSearch, read-only Bash like `ls`) are auto-allowed and never prompt; Write/Edit and non-trivial Bash prompt.

Request (CLI -> us):
```json
{"type":"control_request","request_id":"e1d7cd76-...","request":{
  "subtype":"can_use_tool","tool_name":"Write","display_name":"Write",
  "input":{"file_path":"/abs/hello.txt","content":"hi\n"},
  "description":"hello.txt",
  "permission_suggestions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}],
  "tool_use_id":"toolu_01..."}}
```
Optional extra fields: `decision_reason` / `decision_reason_type` (Bash: "A variable in this command can't be checked before it runs"), `requires_user_interaction:true` (ExitPlanMode). The `assistant` tool_use message arrives **before** the control_request, so tool_start precedes permission_request. The request is keyed by `tool_use_id`, which links it to the tool block.

Allow (verified; the `request_id` lives inside `response`, and the whole thing is wrapped in `response.response`):
```json
{"type":"control_response","response":{"subtype":"success","request_id":"<req id>","response":{"behavior":"allow","updatedInput":{...original or edited input...}}}}
```
`updatedInput` may be omitted and it still works (fixture `permission_allow_noinput`), but send it for safety (SDK schema requires it).

Deny (verified):
```json
{"type":"control_response","response":{"subtype":"success","request_id":"<req id>","response":{"behavior":"deny","message":"User denied this action"}}}
```
Result: `tool_result` with `is_error:true`, `content:"User denied this action"`, meta `permission_decision:{decision:"reject",source:"user_reject"}`. The model then ends its turn normally (the run does not abort). SDK also supports `"interrupt":true` on deny (not tested).

"Always allow" (verified): add `updatedPermissions` to the allow body, e.g.
`"updatedPermissions":[{"type":"addRules","rules":[{"toolName":"Write"}],"behavior":"allow","destination":"session"}]`. Two Writes in one turn then produced only one `can_use_tool` (fixture `permission_always_allow`). `destination:"localSettings"` etc. were not tested; spec's plan to write `.claude/settings.local.json` ourselves is also still valid. The suggestion offered by the CLI (`permission_suggestions`) can be echoed back verbatim as `updatedPermissions` (a `setMode acceptEdits` suggestion = "allow all edits this session").

Error responses: `{"subtype":"error","request_id":..., "error":"..."}` is the generic CLI->client error shape (seen for our requests). We did not test sending one for can_use_tool.

Unanswered requests simply block the turn indefinitely (stream goes quiet); there is no CLI-side timeout observed.

MCP fallback: not needed, not tested.

## 5. Client control requests (all verified, no `initialize` needed)

| subtype | Request | Response (`control_response.response.response`) |
|---|---|---|
| `interrupt` | `{"subtype":"interrupt"}` | `{"still_queued":[]}`. Then the in-flight turn ends with a `result` `subtype:"error_during_execution"`, `is_error:true`, `terminal_reason:"aborted_streaming"`, `errors:["[ede_diagnostic] ..."]`, `total_cost_usd:0`. A synthetic `user` message `[{"type":"text","text":"[Request interrupted by user]"}]` is also emitted. **Process stays alive**; next user turn works. If a `can_use_tool` is pending, the CLI emits `control_cancel_request{request_id}` first. |
| `set_model` | `{"subtype":"set_model","model":"sonnet"}` | success, no body. **Live switch works**: next assistant message had `message.model: "claude-sonnet-5-5"`. Aliases accepted. Bad model -> `{"subtype":"error","error":"Model 'no-such-model' not found","error_code":"not_found"}`. Omitting `model` resets to default (bundle). No confirmation event; track it yourself. A new `init` on the next turn reflects the new `model`. |
| `set_permission_mode` | `{"subtype":"set_permission_mode","mode":"acceptEdits"}` | `{"mode":"acceptEdits"}` + a `system/status` with `permissionMode`. Valid: `acceptEdits, auto, bypassPermissions, default, dontAsk, plan`; else error `invalid_mode`. Takes effect for the next tool call (verified: Write ran with no prompt). |
| `initialize` | `{"subtype":"initialize"}` | Large: `commands, agents, output_style, available_output_styles, models[], account{email,subscriptionType,...}, pid, claude_code_version, current_permission_mode, session_state, capabilities`. Optional. Useful to pre-load the model list. |
| `list_models` | `{"subtype":"list_models"}` | `{"models":[{value,resolvedModel,displayName,description,supportsEffort,supportedEffortLevels,supportsAdaptiveThinking,supportsFastMode,supportsAutoMode}]}`. Values seen: `default, opus, sonnet, fable, haiku` (+ more). Use this instead of a static list. |
| `get_context_usage` | `{"subtype":"get_context_usage"}` | categories, totalTokens, maxTokens, percentage, model... |
| `get_plan` | `{"subtype":"get_plan"}` | `{"exists":true,"content":"<markdown>", ...}` |
| unknown | | `{"subtype":"error","error":"Unsupported control request subtype: nonsense"}` |

Other subtypes present in the binary (bundle, untested): `set_max_thinking_tokens{max_thinking_tokens,thinking_display}`, `apply_flag_settings{settings}`, `get_settings`, `update_settings`, `stop_task`, `reload_plugins`, `rename_session`, `side_question`, `set_cwd`, `read_file`, `mcp_message`, `hook_callback`, `set_proactivity_level`, `get_context_usage`.

Error response shape: `{"type":"control_response","response":{"subtype":"error","request_id":"..","error":"text","error_code":"..?"}}`.

## 6. Plan mode

`--permission-mode plan` (with `--permission-prompt-tool stdio`). The model explores and writes a plan file under `~/.claude/plans/`; Haiku also had to fetch the deferred `ExitPlanMode` via `ToolSearch` first (tool_result is a `tool_reference` block). Edits to the plan file are permitted without prompts. The plan arrives as an ordinary **`can_use_tool` for `tool_name:"ExitPlanMode"`**:
```json
{"subtype":"can_use_tool","tool_name":"ExitPlanMode","input":{"plan":"# Plan...markdown...","planFilePath":"/Users/.../.claude/plans/<slug>.md"},"tool_use_id":"toolu_..","requires_user_interaction":true}
```
Allow it with the normal allow response: CLI emits `system/status {permissionMode:"default"}`, the tool_result says "User has approved your plan...", and the model proceeds to implement (next Write then prompts normally). So plan approval = allow ExitPlanMode; rejection = deny with feedback `message` (not tested, expect the model to revise). Also `get_plan` control request returns the saved plan. Note the plan is also visible earlier in the stream as the `tool_use` input on the `assistant` message and via `input_json_delta`s, i.e. before the permission request: build `plan_proposal` from the control_request (authoritative, has `requires_user_interaction`) and not from the assistant block, to avoid duplicates.

## 7. Resume

- `--resume <sid>` in a new process continues the conversation (secret-word test passed) and **keeps the same session_id** (init and result report the old id).
- Bogus id: the CLI emits one `result` line `{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":0,"session_id":"<the bogus id>","errors":["No conversation found with session ID: <id>"],...}`, prints `No conversation found with session ID: <id>` on **stderr**, and **exits with code 1** (even without sending a user message, no `init`). Detect via exit code / that `errors` string.
- The first process must have flushed its transcript; resume worked after clean stdin-close exit. Not tested after kill -9.

## 8. Bash tool

No streaming of command output. Sequence for `for i in 1 2 3; do echo line$i; sleep 2; done`: assistant tool_use -> `can_use_tool` (Bash prompted here because of a shell variable) -> allow -> ~4s silence apart from `system/task_started` -> `system/task_notification {status:"completed"}` -> single `user` tool_result with the full stdout (`content:"line1\nline2\nline3"`, `tool_use_result.stdout/stderr/interrupted`). Confirmed: no incremental output events. (Background Bash / Monitor tools may differ; not tested.)

## 9. Quirks worth encoding in the parser

1. Multiple `assistant` lines share one `message.id`; merge by id/index, or treat each as a block-done event.
2. `tool_use` input is only complete on the `assistant` line; deltas are partial JSON strings.
3. The replayed `user` line for our own message must not be emitted as a tool result; check `isReplay` or string content.
4. `result` fields are camel/snake mixed (`modelUsage`, `total_cost_usd`, `permissionMode`).
5. Reads are auto-allowed by default mode; the permission UI will rarely see them.
6. After `interrupt`, `result.is_error` is true but it is a normal outcome (`terminal_reason:"aborted_streaming"`); map to `status: interrupted`, not `error`.
7. `rate_limit_event`, `system/status`, `thinking_tokens` are noise lines; ignore unknown types gracefully.

## Spec 09 corrections

1. "`control_response{allow|deny, updatedInput?, updatedPermissions?}`" is imprecise. The real wire form is `{"type":"control_response","response":{"subtype":"success","request_id":ID,"response":{"behavior":"allow"|"deny", "updatedInput"?, "updatedPermissions"?, "message"?}}}`: double-nested `response`, `request_id` in the **outer** `response`, `behavior` (not a bare allow/deny) in the inner. Deny carries `message`. `updatedInput` is optional in practice.
2. Spec 09 says the stdio route needs a Phase-0 check and an MCP fallback: **stdio works**; the MCP fallback is not needed.
3. "`result`: usage and run_result. The tool name `ExitPlanMode` becomes a plan_proposal." ExitPlanMode is not in `result`; it arrives as `control_request can_use_tool` (with `input.plan`, `input.planFilePath`, `requires_user_interaction`). It is the permission request that must be answered to leave plan mode.
4. "`user` with tool_result blocks ... An Edit/Write gets a diff computed from its input": the CLI already provides `tool_use_result.structuredPatch` (Write/Edit), so computing from input is optional. Also `user` lines include our own replayed prompt (`isReplay:true`); parser must skip them (or use them as message acks).
5. "`assistant`: message_done and tool_start": an `assistant` line is per content block, not per message. `message_done` should come from the `message_delta` (stop_reason) / `message_stop` stream events or the last block; tool_start from a `tool_use` block (full input present).
6. "`system/init`: store the session id and tools": init is emitted at the start of **each turn** (and only once the first user message arrives, not at spawn), and the tool list can grow between inits.
7. "Interrupt becomes `control_request{subtype:interrupt}`": correct; need `request_id`, reply is `{still_queued:[]}`, the turn ends with an error-subtype `result` (not a clean success) and pending `can_use_tool`s are cancelled with `control_cancel_request`. Add `control_cancel_request` to the parser's inputs (clear the pending permission_request -> permission_resolved).
8. Missing from spec: live model switch is available (`set_model`, verified) and `set_permission_mode`, `list_models`; the "static model list" can be replaced/augmented by `list_models` (needs a live process; `initialize` also returns it). Model switching mid-run does not require a restart, so the encoder needs `SetModel` and `SetPermissionMode`.
9. Spec 10 lifecycle: confirmed the CLI stays alive after `result`. Exit on stdin close is code 0. `--resume` with a bad id exits 1 with the stderr text above.
10. `usage{in,out,cache,costUsd}`: `result.usage` is per-turn, but `total_cost_usd` and `modelUsage` are cumulative per process; compute costUsd per turn by differencing.
11. No `initialize` handshake required (the Agent SDK sends one, but it only configures hooks/SDK MCP; plain CLI works without).

## Custom subagents (`--agents`)

`--agents '<json>'` accepts an inline JSON object, `{name: {description, prompt, tools?, model?}}`. Verified: `system/init.agents` then lists the name next to the built-ins. Harness uses this to define the `explorer` subagent (read-only tools, cheap model).
