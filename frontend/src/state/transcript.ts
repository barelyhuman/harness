// Per-session transcript stores. Go builds display-ready blocks (internal/transcript); we only hold them and
// apply patches with a port of Go's `Reduce` (immutable here, so unchanged blocks keep their identity and
// memoised renderers skip them).
import { signal, type Signal } from "@preact/signals";
import { SessionService } from "../lib/bindings/github.com/sid/harness/internal/app";
import type { Block } from "../lib/bindings/github.com/sid/harness/internal/transcript/models";
import { bus } from "../lib/bus";
import { mirrorCommand, mirrorResult } from "../features/terminal/mirror";

export type { Block };

export type Patch = {
  op: "snapshot" | "append" | "update";
  blockId?: string;
  id?: string; // API.md shorthand for delta updates: {id, textDelta}
  parentId?: string;
  afterId?: string;
  block?: Block | null;
  textDelta?: string;
  blocks?: Block[] | null;
};

// ---- reducer (mirror of transcript.Reduce) ----

function insertAfter(list: Block[], afterId: string | undefined, b: Block): Block[] {
  if (afterId) {
    const i = list.findIndex((x) => x.id === afterId);
    if (i >= 0) return [...list.slice(0, i + 1), b, ...list.slice(i + 1)];
  }
  return [...list, b];
}

/** Rebuild the path to block `id` (searching depth first like Go's find). Returns null when absent. */
function edit(list: Block[], id: string, fn: (b: Block) => Block): Block[] | null {
  for (let i = 0; i < list.length; i++) {
    const b = list[i];
    let nb: Block | null = null;
    if (b.id === id) nb = fn(b);
    else if (b.children?.length) {
      const kids = edit(b.children, id, fn);
      if (kids) nb = { ...b, children: kids };
    }
    if (nb) {
      const out = list.slice();
      out[i] = nb;
      return out;
    }
  }
  return null;
}

export function reduce(blocks: Block[], p: Patch): Block[] {
  switch (p.op) {
    case "snapshot":
      return (p.blocks ?? []).slice();
    case "append": {
      const nb = p.block;
      if (!nb) return blocks;
      // Defensive: an append for an id we already hold (getter/snapshot overlap) replaces instead of duplicating.
      if (!p.parentId && blocks.some((b) => b.id === nb.id)) return edit(blocks, nb.id, (o) => ({ ...nb, children: nb.children ?? o.children })) ?? blocks;
      if (!p.parentId) return insertAfter(blocks, p.afterId, nb);
      return edit(blocks, p.parentId, (par) => ({ ...par, children: insertAfter(par.children ?? [], p.afterId, nb) })) ?? blocks;
    }
    case "update": {
      const id = p.blockId || p.id || p.block?.id || "";
      if (!id) return blocks;
      if (p.textDelta) {
        const d = p.textDelta;
        return edit(blocks, id, (b) => ({ ...b, text: (b.text ?? "") + d })) ?? blocks;
      }
      const nb = p.block;
      if (!nb) return blocks;
      return edit(blocks, id, (b) => ({ ...nb, children: b.children })) ?? blocks;
    }
  }
  return blocks;
}

// ---- stores ----

export type TranscriptStore = {
  id: string;
  blocks: Signal<Block[]>;
  loaded: Signal<boolean>;
  dispose: () => void;
};

const LRU = 8;
const stores = new Map<string, TranscriptStore>(); // insertion order = recency

function flat(list: Block[], out: Block[] = []): Block[] {
  for (const b of list) {
    out.push(b);
    if (b.children?.length) flat(b.children, out);
  }
  return out;
}

export function findBlock(list: Block[], id: string): Block | undefined {
  return flat(list).find((b) => b.id === id);
}

// ---- agent mirror (spec 06): every Bash tool is mirrored once at start and once at completion ----

type Mirrored = { started: Set<string>; done: Set<string> };

function mirrorScan(sessionId: string, m: Mirrored, list: Block[], live: boolean) {
  const scopeKey = `session:${sessionId}`;
  for (const b of flat(list)) {
    const t = b.type === "tool" ? b.tool : null;
    if (!t || t.kind !== "bash") continue;
    const cmd = t.command ?? "";
    const finished = t.status === "ok" || t.status === "error";
    if (!live) {
      // History on first load: never replay completed commands into the mirror.
      if (finished) { m.started.add(b.id); m.done.add(b.id); continue; }
    }
    if (t.status === "pending" && !finished) continue; // awaiting permission / input still streaming
    if (!cmd) continue;
    if (!m.started.has(b.id)) {
      m.started.add(b.id);
      mirrorCommand(scopeKey, cmd);
    }
    if (finished && !m.done.has(b.id)) {
      m.done.add(b.id);
      if (!t.denied) mirrorResult(scopeKey, t.output ?? "", t.exitCode ?? (t.status === "ok" ? 0 : 1));
    }
  }
}

function create(id: string): TranscriptStore {
  const blocks = signal<Block[]>([]);
  const loaded = signal(false);
  const mirrored: Mirrored = { started: new Set(), done: new Set() };
  let gotSnapshot = false;
  let initial = false; // getter result applied or superseded
  let buffered: Patch[] = [];
  let disposed = false;
  const chans = new Set<() => void>();

  const apply = (p: Patch) => {
    blocks.value = reduce(blocks.value, p);
    mirrorScan(id, mirrored, blocks.value, true);
  };

  const unOpen = bus.onOpen(`blocks/${id}`, (ch) => {
    if (ch.name !== `blocks/${id}`) return;
    const un = ch.onJSON<Patch>((p) => {
      if (disposed || !p) return;
      if (p.op === "snapshot") {
        gotSnapshot = true;
        initial = true;
        buffered = [];
        const wasLoaded = loaded.value;
        blocks.value = reduce(blocks.value, p);
        mirrorScan(id, mirrored, blocks.value, wasLoaded);
        loaded.value = true;
      } else if (!initial) buffered.push(p);
      else apply(p);
    });
    chans.add(un);
  });

  void SessionService.Transcript(id)
    .then((bs) => {
      if (disposed || gotSnapshot) return;
      initial = true;
      blocks.value = (bs ?? []).slice();
      mirrorScan(id, mirrored, blocks.value, false);
      for (const p of buffered) apply(p);
      buffered = [];
      loaded.value = true;
    })
    .catch(() => {
      if (!disposed) loaded.value = true;
    });

  return {
    id,
    blocks,
    loaded,
    dispose() {
      disposed = true;
      unOpen();
      chans.forEach((u) => u());
    },
  };
}

/** Get (and touch) the store of a session; evicts the least recently viewed beyond 8. */
export function transcriptFor(sessionId: string): TranscriptStore {
  let s = stores.get(sessionId);
  if (s) {
    stores.delete(sessionId);
    stores.set(sessionId, s);
    return s;
  }
  s = create(sessionId);
  stores.set(sessionId, s);
  while (stores.size > LRU) {
    const [oldId, old] = stores.entries().next().value!;
    stores.delete(oldId);
    old.dispose();
  }
  return s;
}
