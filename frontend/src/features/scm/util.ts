import type { ComponentType } from "preact";
import { IconBraces, IconBrandGolang, IconBrandPython, IconFileLock, IconFileSettings, IconFileText, IconFileTypeCss, IconFileTypeHtml, IconFileTypeJs, IconFileTypeJsx, IconFileTypeRs, IconFileTypeSql, IconFileTypeSvg, IconFileTypeTs, IconFileTypeTsx, IconMarkdown, IconPhoto, IconTerminal2 } from "@tabler/icons-preact";
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

type FileIconC = ComponentType<{ size?: number; stroke?: number; color?: string }>;
const EXT_ICON: Record<string, [FileIconC, string]> = {
  ts: [IconFileTypeTs, "#3b9cdc"], tsx: [IconFileTypeTsx, "#3b9cdc"], js: [IconFileTypeJs, "#e5c04b"], jsx: [IconFileTypeJsx, "#e5c04b"], mjs: [IconFileTypeJs, "#e5c04b"],
  go: [IconBrandGolang, "#4fc3d9"], rs: [IconFileTypeRs, "#dea584"], py: [IconBrandPython, "#5b9bd5"], json: [IconBraces, "#cbcb41"], md: [IconMarkdown, "#6a9fd4"],
  css: [IconFileTypeCss, "#a074c4"], html: [IconFileTypeHtml, "#e37933"], yml: [IconFileSettings, "#cb6b6b"], yaml: [IconFileSettings, "#cb6b6b"], sh: [IconTerminal2, "#8dc149"],
  toml: [IconFileSettings, "#9c9c9c"], sql: [IconFileTypeSql, "#dad8d8"], svg: [IconFileTypeSvg, "#ffb13b"], png: [IconPhoto, "#a074c4"], lock: [IconFileLock, "#9c9c9c"],
};
export function fileIcon(name: string): [FileIconC, string] {
  const i = name.lastIndexOf(".");
  const ext = i >= 0 ? name.slice(i + 1).toLowerCase() : "";
  return EXT_ICON[ext] ?? [IconFileText, "#9da5b4"];
}
