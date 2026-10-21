// Agent mirror terminal: read-only xterm (no pty), fed by the chat agent's Bash tool events.
//
// DESIGN NOTE: Go's NewTab has no `kind` parameter, so an agent tab cannot be created through
// TermService. Until it does, the mirror is shown in a frontend-only "Agent" tab managed here
// (agentTabEnabled/agentTabSelected in state.ts). This is an acceptable exception to "Go owns
// logic" because it is display-only: no PTY, no persistence, no layout semantics. If Go later
// pushes a leaf with kind "agent", that leaf renders the same per-scope mirror terminal.
import { agentTabEnabled, agentTabSelected, setScopeFlag } from "./state";
import { entries, makeEntry, type Entry } from "./terms";

const MAX_BUF = 1024 * 1024;
const pending = new Map<string, string>();

const norm = (s: string) => s.replace(/\r?\n/g, "\r\n");

/** Per-scope mirror entry, created when an agent pane is first shown. */
export function getMirrorEntry(scopeKey: string): Entry {
  const key = "mirror:" + scopeKey;
  let e = entries.get(key);
  if (!e) {
    e = makeEntry(key, scopeKey, true);
    e.term.write("\x1b[?25l");
    const buf = pending.get(scopeKey);
    if (buf) {
      e.term.write(buf);
      pending.delete(scopeKey);
    }
  }
  return e;
}

/** Append raw text (ANSI allowed) to the scope's agent terminal; buffers (<=1MB) until shown. */
export function mirrorWrite(scopeKey: string, text: string): void {
  setScopeFlag(agentTabEnabled, scopeKey, true);
  const t = norm(text);
  const e = entries.get("mirror:" + scopeKey);
  if (e) {
    e.term.write(t);
    return;
  }
  let b = (pending.get(scopeKey) ?? "") + t;
  if (b.length > MAX_BUF) b = b.slice(b.length - MAX_BUF);
  pending.set(scopeKey, b);
}

/** `$ cmd` in bold. */
export function mirrorCommand(scopeKey: string, cmd: string): void {
  mirrorWrite(scopeKey, `\x1b[1m$ ${cmd}\x1b[0m\n`);
}

/** Output followed by a green/red exit status line. */
export function mirrorResult(scopeKey: string, output: string, exitCode: number): void {
  const out = output && !output.endsWith("\n") ? output + "\n" : output;
  const status = exitCode === 0 ? "\x1b[32m✓ exit 0\x1b[0m" : `\x1b[31m✗ exit ${exitCode}\x1b[0m`;
  mirrorWrite(scopeKey, `${out}${status}\n\n`);
}

/** Make sure the Agent terminal exists in the scope (frontend-only tab) and, by default, select it. */
export async function ensureAgentTerminal(scopeKey: string, focus = true): Promise<void> {
  setScopeFlag(agentTabEnabled, scopeKey, true);
  if (focus) setScopeFlag(agentTabSelected, scopeKey, true);
}
