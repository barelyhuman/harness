// LSP bridge public API. Independent of the UI: the host injects toast + model loading.
import { signal } from "@preact/signals";
import type * as M from "monaco-editor";
import { ensureMonaco } from "../monaco-setup";
import { LspClient, type BridgeHost } from "./client";
import { registerProviders } from "./providers";

export type { BridgeHost } from "./client";

let host: BridgeHost = { notify: (m) => console.warn("[lsp]", m), ensureModel: async () => {} };
export function configureLsp(h: BridgeHost): void {
  host = h;
}

const clients = new Map<string, LspClient>();
const byModel = new Map<string, LspClient>();

/** Problems count for a status bar. */
export const diagnosticCounts = signal({ errors: 0, warnings: 0 });
let markersHooked = false;
function hookMarkers() {
  if (markersHooked) return;
  markersHooked = true;
  const m = ensureMonaco();
  m.editor.onDidChangeMarkers(() => {
    let errors = 0, warnings = 0;
    for (const mk of m.editor.getModelMarkers({})) {
      if (mk.severity === m.MarkerSeverity.Error) errors++;
      else if (mk.severity === m.MarkerSeverity.Warning) warnings++;
    }
    diagnosticCounts.value = { errors, warnings };
  });
}

const ctx = { clientOf: (model: M.editor.ITextModel) => byModel.get(model.uri.toString()), get host() { return host; } };

/**
 * Attach a model to the language server for (root, model language). Returns a detach function.
 * No-ops (but stays safe) when the LSPService binding or server is unavailable.
 */
export function attachModel(model: M.editor.ITextModel, root: string, path: string): () => void {
  hookMarkers();
  const lang = model.getLanguageId();
  if (!lang || lang === "plaintext") return () => {};
  const id = root + "\n" + lang;
  let c = clients.get(id);
  if (!c) {
    c = new LspClient(root, lang, host, (cl) => {
      if (cl.caps) registerProviders(cl.lang, cl.caps, ctx, cl);
    });
    clients.set(id, c);
  }
  const uri = model.uri.toString();
  byModel.set(uri, c);
  c.attach(model, path);
  const client = c;
  const off = model.onWillDispose(() => detach());
  let done = false;
  function detach() {
    if (done) return;
    done = true;
    off.dispose();
    client.detach(model);
    if (!client.docs.size) {
      // keep idle clients around briefly so reopening a file is instant; Go owns the server TTL.
      setTimeout(() => {
        if (!client.docs.size && clients.get(id) === client) { client.dispose(); clients.delete(id); }
      }, 60_000);
    }
    if (byModel.get(uri) === client && !client.docs.has(uri)) byModel.delete(uri);
  }
  return detach;
}

export function lspDidSave(model: M.editor.ITextModel): void {
  byModel.get(model.uri.toString())?.didSave(model);
}
