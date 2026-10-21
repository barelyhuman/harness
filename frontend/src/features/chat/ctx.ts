import { createContext } from "preact";
import { signal } from "@preact/signals";
import { SessionService } from "../../lib/bindings/github.com/sid/harness/internal/app";
import { activeScope } from "../../state/scope";

export type ChatCtxT = { sessionId: string; scopeKey: string; pendingReq: string | null };
export const ChatCtx = createContext<ChatCtxT>({ sessionId: "", scopeKey: "", pendingReq: null });

const roots = signal<Record<string, string>>({});
const inflight = new Set<string>();

/** Scope root for a session (sync from cache / activeScope, else fetched once). Reactive. */
export function scopeRoot(sessionId: string): string {
  const a = activeScope.value;
  if (a && a.sessionId === sessionId && a.root) return a.root;
  const r = roots.value[sessionId];
  if (r) return r;
  if (!inflight.has(sessionId)) {
    inflight.add(sessionId);
    void SessionService.Scope(sessionId)
      .then((s) => { roots.value = { ...roots.value, [sessionId]: s.root }; })
      .catch(() => {})
      .finally(() => inflight.delete(sessionId));
  }
  return "";
}

export function absPath(root: string, p: string): string {
  if (!p) return p;
  if (p.startsWith("/")) return p;
  return root ? root.replace(/\/$/, "") + "/" + p.replace(/^\.\//, "") : p;
}

export function relPath(root: string, p: string): string {
  return root && p.startsWith(root.replace(/\/$/, "") + "/") ? p.slice(root.length + 1) : p;
}
