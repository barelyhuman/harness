// Toast system. Any module may `import { toast } from "../state/toast"`.
import { signal } from "@preact/signals";

export type ToastKind = "info" | "success" | "error";
export type ToastItem = { id: number; msg: string; kind: ToastKind };
export const toasts = signal<ToastItem[]>([]);
let seq = 1;

export function toast(msg: string, kind: ToastKind = "info", ms = kind === "error" ? 6000 : 3500): void {
  const id = seq++;
  toasts.value = [...toasts.value, { id, msg, kind }].slice(-5);
  setTimeout(() => dismissToast(id), ms);
}
export function dismissToast(id: number): void {
  toasts.value = toasts.value.filter((t) => t.id !== id);
}

/** Decode a rejected Wails call: Go errors carry `{code, message, files?, etag?}` in `cause`. */
export type GoError = { code: string; message: string; files?: string[]; etag?: string };
export function errInfo(e: unknown): GoError {
  const x = e as any;
  let c = x?.cause ?? x;
  if (typeof c === "string") { try { c = JSON.parse(c); } catch { /* keep */ } }
  if (c && typeof c === "object" && typeof c.code === "string") return { code: c.code, message: c.message ?? "", files: c.files, etag: c.etag };
  if (typeof x?.message === "string") {
    try { const j = JSON.parse(x.message); if (j && typeof j.code === "string") return { code: j.code, message: j.message ?? "", files: j.files, etag: j.etag }; } catch { /* keep */ }
  }
  return { code: "internal", message: String(x?.message ?? e) };
}
export function toastError(e: unknown): void {
  toast(errInfo(e).message || "Something went wrong", "error");
}
