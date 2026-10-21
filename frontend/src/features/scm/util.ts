import { bus } from "../../lib/bus";

export type ErrInfo = { code: string; message: string; files?: string[]; etag?: string };

/** Normalise a rejected Wails call into the {code, message} contract shape. */
export function errInfo(e: any): ErrInfo {
  let c = e?.cause ?? e;
  if (typeof c === "string") {
    try {
      c = JSON.parse(c);
    } catch {
      /* plain string */
    }
  }
  if (c && typeof c === "object" && "code" in c) return { code: c.code, message: c.message ?? String(c.code), files: c.files, etag: c.etag };
  return { code: "internal", message: (typeof c === "string" ? c : c?.message) ?? e?.message ?? String(e) };
}

/** Subscribe to a server-opened JSON channel by exact name. */
export function subscribeJSON<T>(name: string, fn: (v: T) => void): () => void {
  const subs = new Map<unknown, () => void>();
  const off = bus.onOpen(name, (ch) => {
    if (ch.name !== name) return;
    subs.set(ch, ch.onJSON<T>((v) => v !== undefined && fn(v)));
  });
  return () => {
    off();
    subs.forEach((u) => u());
    subs.clear();
  };
}

export const joinPath = (root: string, rel: string) => rel.startsWith("/") ? rel : root.replace(/\/+$/, "") + "/" + rel;

// VS Code dark+ SCM decoration colours, by Go's row class.
export const CLASS_COLOR: Record<string, string> = {
  "scm-modified": "#e2c08d",
  "scm-added": "#81b88b",
  "scm-deleted": "#c74e39",
  "scm-renamed": "#73c991",
  "scm-untracked": "#73c991",
  "scm-conflict": "#e4676b",
};

const EXT_ICON: Record<string, [string, string]> = {
  ts: ["TS", "#3b9cdc"], tsx: ["TX", "#3b9cdc"], js: ["JS", "#e5c04b"], jsx: ["JX", "#e5c04b"], mjs: ["JS", "#e5c04b"],
  go: ["GO", "#4fc3d9"], rs: ["RS", "#dea584"], py: ["PY", "#5b9bd5"], json: ["{}", "#cbcb41"], md: ["M↓", "#6a9fd4"],
  css: ["#", "#a074c4"], html: ["<>", "#e37933"], yml: ["Y", "#cb6b6b"], yaml: ["Y", "#cb6b6b"], sh: ["$", "#8dc149"],
  toml: ["T", "#9c9c9c"], sql: ["Q", "#dad8d8"], svg: ["S", "#ffb13b"], png: ["▣", "#a074c4"], lock: ["L", "#9c9c9c"],
};
export function fileIcon(name: string): [string, string] {
  const i = name.lastIndexOf(".");
  const ext = i >= 0 ? name.slice(i + 1).toLowerCase() : "";
  return EXT_ICON[ext] ?? ["≡", "#9da5b4"];
}
