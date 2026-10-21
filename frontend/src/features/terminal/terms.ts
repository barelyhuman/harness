// xterm instance cache. Terminals live in a module-level Map keyed by ptyId so PTYs/buffers
// survive scope switches (spec 03 LRU); a pane re-parents the existing element on mount.
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { WebglAddon } from "@xterm/addon-webgl";
import "@xterm/xterm/css/xterm.css";
import { bus, type BusChannel } from "../../lib/bus";
import { ClosePane } from "../../lib/bindings/github.com/sid/harness/internal/app/termservice";
import { openFile } from "../../state/editor";
import { isReservedChord } from "./keys";
import { scopeRoots, leaves, layouts } from "./state";

export type Entry = {
  key: string;
  scopeKey: string;
  term: Terminal;
  fit: FitAddon;
  search: SearchAddon;
  el: HTMLDivElement;
  ch?: BusChannel;
  paneId: string;
  exited: boolean;
  opened: boolean;
  createdAt: number;
  readOnly: boolean;
  dispose: () => void;
};

export const entries = new Map<string, Entry>();

export const THEME = {
  background: "#141414", foreground: "#d4d4d4", cursor: "#d4d4d4", cursorAccent: "#141414",
  selectionBackground: "#3a5a8c80", selectionInactiveBackground: "#ffffff22",
  black: "#1e1e1e", red: "#f4727c", green: "#7fd48a", yellow: "#e5c07b", blue: "#6aa3ff", magenta: "#c792ea", cyan: "#56c7d4", white: "#d4d4d4",
  brightBlack: "#6b6b6b", brightRed: "#ff8b94", brightGreen: "#9be8a5", brightYellow: "#f0d397", brightBlue: "#8ab8ff", brightMagenta: "#dba9f5", brightCyan: "#7fdde8", brightWhite: "#ffffff",
};

function makeEntry(key: string, scopeKey: string, readOnly: boolean): Entry {
  const term = new Terminal({
    fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
    fontSize: 12.5, lineHeight: 1.15, theme: THEME, scrollback: 10000, cursorBlink: !readOnly,
    allowProposedApi: true, macOptionIsMeta: false, disableStdin: readOnly, convertEol: readOnly,
  });
  const fit = new FitAddon();
  const search = new SearchAddon();
  term.loadAddon(fit);
  term.loadAddon(search);
  const el = document.createElement("div");
  el.style.cssText = "width:100%;height:100%";
  const e: Entry = {
    key, scopeKey, term, fit, search, el, paneId: "", exited: false, opened: false, createdAt: Date.now(), readOnly,
    dispose: () => {
      e.ch?.close();
      term.dispose();
      el.remove();
      entries.delete(key);
    },
  };
  term.attachCustomKeyEventHandler((ev) => !(ev.type === "keydown" && isReservedChord(ev)));
  registerLinks(e);
  entries.set(key, e);
  return e;
}

/** Open xterm into its element once it is in the DOM (webgl needs real dimensions). */
export function ensureOpened(e: Entry) {
  if (e.opened) return;
  e.opened = true;
  e.term.open(e.el);
  try {
    const gl = new WebglAddon();
    gl.onContextLoss(() => gl.dispose());
    e.term.loadAddon(gl);
  } catch {
    /* DOM renderer fallback */
  }
}

export function getPtyEntry(ptyId: string, scopeKey: string): Entry {
  const ex = entries.get(ptyId);
  if (ex) return ex;
  const e = makeEntry(ptyId, scopeKey, false);
  const ch = bus.open("pty/" + ptyId);
  e.ch = ch;
  const enc = new TextEncoder();
  ch.onBytes((b) => e.term.write(b));
  ch.onJSON<{ type?: string; code?: number }>((m) => {
    if (m?.type === "exit") {
      e.exited = true;
      e.term.write(`\r\n\x1b[2m[process exited with code ${m.code ?? 0}]\x1b[0m\r\n\x1b[2mpress Enter to close this pane\x1b[0m\r\n`);
    }
  });
  e.term.onData((d) => {
    if (e.exited) {
      if (d === "\r" && e.paneId) void ClosePane(e.scopeKey, e.paneId);
      return;
    }
    ch.sendBytes(enc.encode(d));
  });
  let t: ReturnType<typeof setTimeout> | undefined;
  e.term.onResize(({ cols, rows }) => {
    clearTimeout(t);
    t = setTimeout(() => ch.sendJSON({ type: "resize", cols, rows }), 60);
  });
  return e;
}

/** Fit to the host and push the size to the pty. */
export function fitEntry(e: Entry) {
  if (!e.opened || !e.el.isConnected || e.el.clientWidth < 8 || e.el.clientHeight < 8) return;
  try {
    const d = e.fit.proposeDimensions();
    if (d && (d.cols !== e.term.cols || d.rows !== e.term.rows)) e.fit.fit();
  } catch {
    /* not measurable yet */
  }
}

/** Dispose cached terminals of a scope whose pty is no longer in the layout. */
export function reconcile(scopeKey: string) {
  const l = layouts.value[scopeKey];
  if (!l) return;
  const live = new Set<string>();
  for (const t of l.tabs ?? []) for (const n of leaves(t.root)) if (n.ptyId) live.add(n.ptyId);
  for (const e of [...entries.values()]) {
    if (e.scopeKey === scopeKey && !e.readOnly && !live.has(e.key)) e.dispose();
  }
}

const URL_RE = /https?:\/\/[^\s<>"'`]+[^\s<>"'`.,;:!?)\]}]/g;
const PATH_RE = /(?:~?\.{0,2}\/)?(?:[\w.@+~-]+\/)+[\w.@+~-]+(?::(\d+))?(?::(\d+))?|(?:\.{0,2}\/)?[\w@+~-][\w.@+~-]*\.[A-Za-z][A-Za-z0-9]*:(\d+)(?::(\d+))?/g;

function resolvePath(scopeKey: string, p: string): string | null {
  if (p.startsWith("~")) return null;
  const clean = p.replace(/:\d+(:\d+)?$/, "");
  if (clean.startsWith("/")) return clean;
  const root = scopeRoots.get(scopeKey);
  if (!root) return null;
  const parts = (root + "/" + clean).split("/");
  const out: string[] = [];
  for (const s of parts) {
    if (s === "" || s === ".") continue;
    if (s === "..") out.pop();
    else out.push(s);
  }
  return "/" + out.join("/");
}

function registerLinks(e: Entry) {
  e.term.registerLinkProvider({
    provideLinks(y, cb) {
      const line = e.term.buffer.active.getLine(y - 1);
      if (!line) return cb(undefined);
      const text = line.translateToString(true);
      const links: any[] = [];
      const taken: [number, number][] = [];
      const range = (i: number, len: number) => ({ start: { x: i + 1, y }, end: { x: i + len, y } });
      for (const m of text.matchAll(URL_RE)) {
        taken.push([m.index!, m.index! + m[0].length]);
        const url = m[0];
        links.push({ range: range(m.index!, url.length), text: url, activate: () => window.open(url, "_blank") });
      }
      for (const m of text.matchAll(PATH_RE)) {
        const s = m.index!, en = s + m[0].length;
        if (taken.some(([a, b]) => s < b && en > a)) continue;
        const abs = resolvePath(e.scopeKey, m[0]);
        if (!abs) continue;
        links.push({ range: range(s, m[0].length), text: m[0], activate: () => openFile(e.scopeKey, abs) });
      }
      cb(links.length ? links : undefined);
    },
  });
}

export { makeEntry };
