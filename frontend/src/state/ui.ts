// Overlay state (palette, quick open, sheets, picker). One overlay at a time.
import { effect, signal } from "@preact/signals";
import { setContext } from "../lib/commands";
import type { Role } from "./roles";

export type Overlay =
  | { kind: "palette" }
  | { kind: "quickopen" }
  | { kind: "picker" }
  | { kind: "projectsettings"; projectId: string; role?: Role }
  | { kind: "quit"; shells: number };

export const overlay = signal<Overlay | null>(null);
export const openOverlay = (o: Overlay) => { overlay.value = o; };
export const closeOverlay = () => { overlay.value = null; };
export const toggleOverlay = (o: Overlay) => { overlay.value = overlay.value?.kind === o.kind ? null : o; };

effect(() => setContext("pickerOpen", overlay.value !== null));
