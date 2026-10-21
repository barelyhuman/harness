// Monaco language providers backed by LspClient. Registered once per Monaco language id; each
// handler routes by model to the client that owns it and bails when the server lacks the capability.
import type * as lsp from "vscode-languageserver-protocol";
import type * as M from "monaco-editor";
import { ensureMonaco } from "../monaco-setup";
import type { BridgeHost, LspClient } from "./client";
import {
  docString, fromRange, locationUri, markdown, symbolKind, toCompletionItem, toLocation, toPos, toRange,
  toTextEdits, toWorkspaceEdit, workspaceEditUris, type CompletionExtra,
} from "./convert";

type Ctx = { clientOf(model: M.editor.ITextModel): LspClient | undefined; host: BridgeHost };
const registered = new Set<string>();
let cmdRegistered = false;
const clientsById = new Map<string, LspClient>();

const tdp = (model: M.editor.ITextModel, pos: M.IPosition) => ({ textDocument: { uri: model.uri.toString() }, position: toPos(pos) });

export function registerProviders(lang: string, caps: lsp.ServerCapabilities, ctx: Ctx, client: LspClient): void {
  clientsById.set(client.id, client);
  const m = ensureMonaco();
  if (!cmdRegistered) {
    cmdRegistered = true;
    m.editor.registerCommand("harness.lsp.executeCommand", (_acc, clientId: string, command: lsp.Command) => {
      void clientsById.get(clientId)?.request("workspace/executeCommand", { command: command.command, arguments: command.arguments });
    });
  }
  if (registered.has(lang)) return;
  registered.add(lang);
  const L = m.languages;
  const withClient = <T>(model: M.editor.ITextModel, cap: (c: lsp.ServerCapabilities) => unknown, fn: (c: LspClient) => Promise<T | undefined>) => {
    const c = ctx.clientOf(model);
    if (!c?.caps || !cap(c.caps)) return Promise.resolve(undefined);
    return fn(c);
  };

  if (caps.completionProvider) {
    L.registerCompletionItemProvider(lang, {
      triggerCharacters: caps.completionProvider.triggerCharacters,
      async provideCompletionItems(model, pos, context, token) {
        return withClient(model, (c) => c.completionProvider, async (c) => {
          const word = model.getWordUntilPosition(pos);
          const wordRange: M.IRange = { startLineNumber: pos.lineNumber, endLineNumber: pos.lineNumber, startColumn: word.startColumn, endColumn: word.endColumn };
          const res = await c.request<lsp.CompletionItem[] | lsp.CompletionList>("textDocument/completion", {
            ...tdp(model, pos),
            context: { triggerKind: context.triggerKind + 1, triggerCharacter: context.triggerCharacter },
          }, token, model.uri.toString());
          if (!res) return undefined;
          const items = Array.isArray(res) ? res : res.items;
          const defaults = Array.isArray(res) ? undefined : res.itemDefaults;
          return { suggestions: items.map((i) => toCompletionItem(i, defaults, wordRange, model)), incomplete: !Array.isArray(res) && res.isIncomplete };
        });
      },
      async resolveCompletionItem(item, token) {
        const raw = (item as CompletionExtra)._lsp;
        const c = [...clientsById.values()].find((x) => x.lang === lang && x.caps?.completionProvider?.resolveProvider);
        if (!raw || !c) return item;
        const r = await c.request<lsp.CompletionItem>("completionItem/resolve", raw, token);
        if (r) {
          item.documentation = docString(r.documentation) ?? item.documentation;
          item.detail = r.detail ?? item.detail;
          if (r.additionalTextEdits) item.additionalTextEdits = toTextEdits(r.additionalTextEdits);
        }
        return item;
      },
    });
  }
  if (caps.hoverProvider) {
    L.registerHoverProvider(lang, {
      provideHover: (model, pos, token) => withClient(model, (c) => c.hoverProvider, async (c) => {
        const h = await c.request<lsp.Hover>("textDocument/hover", tdp(model, pos), token, model.uri.toString());
        if (!h?.contents) return undefined;
        const arr = Array.isArray(h.contents) ? h.contents : [h.contents];
        const contents = arr.map(markdown).filter((x): x is M.IMarkdownString => !!x && x.value !== "");
        if (!contents.length) return undefined;
        return { contents, range: h.range ? fromRange(h.range) : undefined };
      }),
    });
  }
  if (caps.signatureHelpProvider) {
    L.registerSignatureHelpProvider(lang, {
      signatureHelpTriggerCharacters: caps.signatureHelpProvider.triggerCharacters,
      signatureHelpRetriggerCharacters: caps.signatureHelpProvider.retriggerCharacters,
      provideSignatureHelp: (model, pos, token, context) => withClient(model, (c) => c.signatureHelpProvider, async (c) => {
        const r = await c.request<lsp.SignatureHelp>("textDocument/signatureHelp", {
          ...tdp(model, pos),
          context: { triggerKind: context.triggerKind, triggerCharacter: context.triggerCharacter, isRetrigger: context.isRetrigger },
        }, token, model.uri.toString());
        if (!r?.signatures?.length) return undefined;
        const value: M.languages.SignatureHelp = {
          activeSignature: r.activeSignature ?? 0,
          activeParameter: r.activeParameter ?? 0,
          signatures: r.signatures.map((s) => ({
            label: s.label,
            documentation: docString(s.documentation),
            activeParameter: s.activeParameter ?? undefined,
            parameters: (s.parameters ?? []).map((p) => ({ label: p.label, documentation: docString(p.documentation) })),
          })),
        };
        return { value, dispose() {} };
      }),
    });
  }
  const locs = async (c: LspClient, method: string, params: unknown, token: M.CancellationToken, uri: string) => {
    const r = await c.request<lsp.Location | lsp.Location[] | lsp.LocationLink[]>(method, params, token, uri);
    if (!r) return undefined;
    const arr = Array.isArray(r) ? r : [r];
    await Promise.all(arr.map((l) => ctx.host.ensureModel(m.Uri.parse(locationUri(l)))));
    return arr.map(toLocation);
  };
  if (caps.definitionProvider) {
    L.registerDefinitionProvider(lang, {
      provideDefinition: (model, pos, token) => withClient(model, (c) => c.definitionProvider, async (c) =>
        locs(c, "textDocument/definition", tdp(model, pos), token, model.uri.toString())),
    });
  }
  if (caps.referencesProvider) {
    L.registerReferenceProvider(lang, {
      provideReferences: (model, pos, context, token) => withClient(model, (c) => c.referencesProvider, async (c) => {
        const r = await locs(c, "textDocument/references", { ...tdp(model, pos), context: { includeDeclaration: context.includeDeclaration } }, token, model.uri.toString());
        return r as M.languages.Location[] | undefined;
      }),
    });
  }
  if (caps.documentSymbolProvider) {
    L.registerDocumentSymbolProvider(lang, {
      provideDocumentSymbols: (model, token) => withClient(model, (c) => c.documentSymbolProvider, async (c) => {
        const r = await c.request<(lsp.DocumentSymbol | lsp.SymbolInformation)[]>("textDocument/documentSymbol", { textDocument: { uri: model.uri.toString() } }, token, model.uri.toString());
        if (!r) return undefined;
        const conv = (s: lsp.DocumentSymbol | lsp.SymbolInformation): M.languages.DocumentSymbol => {
          if ("location" in s) {
            const rg = fromRange(s.location.range);
            return { name: s.name, detail: "", kind: symbolKind(s.kind), tags: [], containerName: s.containerName, range: rg, selectionRange: rg };
          }
          return {
            name: s.name, detail: s.detail ?? "", kind: symbolKind(s.kind), tags: s.tags?.includes(1) ? [1] : [],
            range: fromRange(s.range), selectionRange: fromRange(s.selectionRange), children: s.children?.map(conv),
          };
        };
        return r.map(conv);
      }),
    });
  }
  if (caps.renameProvider) {
    const prep = typeof caps.renameProvider === "object" && caps.renameProvider.prepareProvider;
    L.registerRenameProvider(lang, {
      resolveRenameLocation: prep ? (model, pos, token) => withClient(model, (c) => c.renameProvider, async (c) => {
        const r = await c.request<lsp.Range | { range: lsp.Range; placeholder: string } | { defaultBehavior: boolean }>("textDocument/prepareRename", tdp(model, pos), token, model.uri.toString());
        const w = model.getWordAtPosition(pos);
        const wordRange: M.IRange = w ? { startLineNumber: pos.lineNumber, endLineNumber: pos.lineNumber, startColumn: w.startColumn, endColumn: w.endColumn } : { startLineNumber: pos.lineNumber, endLineNumber: pos.lineNumber, startColumn: pos.column, endColumn: pos.column };
        if (!r) return { range: wordRange, text: "", rejectReason: "Cannot rename this element" };
        const rg = "range" in r ? r.range : "start" in r ? r : undefined;
        if (!rg) return w ? { range: wordRange, text: w.word } : undefined;
        const range = fromRange(rg);
        return { range, text: "placeholder" in r ? r.placeholder : model.getValueInRange(range) };
      }) : undefined,
      provideRenameEdits: (model, pos, newName, token) => withClient(model, (c) => c.renameProvider, async (c) => {
        const r = await c.request<lsp.WorkspaceEdit>("textDocument/rename", { ...tdp(model, pos), newName }, token, model.uri.toString());
        if (!r) return undefined;
        await Promise.all(workspaceEditUris(r).map((u) => ctx.host.ensureModel(m.Uri.parse(u))));
        return toWorkspaceEdit(r);
      }),
    });
  }
  if (caps.documentFormattingProvider) {
    L.registerDocumentFormattingEditProvider(lang, {
      provideDocumentFormattingEdits: (model, options, token) => withClient(model, (c) => c.documentFormattingProvider, async (c) => {
        const r = await c.request<lsp.TextEdit[]>("textDocument/formatting", { textDocument: { uri: model.uri.toString() }, options: { tabSize: options.tabSize, insertSpaces: options.insertSpaces } }, token, model.uri.toString());
        return r ? toTextEdits(r) : undefined;
      }),
    });
  }
  if (caps.documentRangeFormattingProvider) {
    L.registerDocumentRangeFormattingEditProvider(lang, {
      provideDocumentRangeFormattingEdits: (model, range, options, token) => withClient(model, (c) => c.documentRangeFormattingProvider, async (c) => {
        const r = await c.request<lsp.TextEdit[]>("textDocument/rangeFormatting", { textDocument: { uri: model.uri.toString() }, range: toRange(range), options: { tabSize: options.tabSize, insertSpaces: options.insertSpaces } }, token, model.uri.toString());
        return r ? toTextEdits(r) : undefined;
      }),
    });
  }
  if (caps.codeActionProvider) {
    const resolvable = typeof caps.codeActionProvider === "object" && !!caps.codeActionProvider.resolveProvider;
    const conv = (client: LspClient, a: lsp.Command | lsp.CodeAction): M.languages.CodeAction => {
      const ca: lsp.CodeAction = "command" in a && typeof a.command === "string" ? { title: a.title, command: a as lsp.Command } : (a as lsp.CodeAction);
      const out: M.languages.CodeAction & { _lsp?: lsp.CodeAction } = {
        title: ca.title,
        kind: ca.kind,
        isPreferred: ca.isPreferred,
        disabled: ca.disabled?.reason,
        edit: ca.edit ? toWorkspaceEdit(ca.edit) : undefined,
        command: ca.command ? { id: "harness.lsp.executeCommand", title: ca.command.title, arguments: [client.id, ca.command] } : undefined,
        _lsp: ca,
      };
      return out;
    };
    L.registerCodeActionProvider(lang, {
      async provideCodeActions(model, range, context, token) {
        const r = await withClient(model, (c) => c.codeActionProvider, async (c) => {
          const uri = model.uri.toString();
          const lr = toRange(range);
          const diagnostics = (c.diag.get(uri) ?? []).filter((d) => !(d.range.end.line < lr.start.line || d.range.start.line > lr.end.line));
          const res = await c.request<(lsp.Command | lsp.CodeAction)[]>("textDocument/codeAction", {
            textDocument: { uri }, range: lr,
            context: { diagnostics, only: context.only ? [context.only] : undefined, triggerKind: context.trigger },
          }, token, uri);
          if (!res) return undefined;
          return res.map((a) => conv(c, a));
        });
        return r ? { actions: r, dispose() {} } : undefined;
      },
      resolveCodeAction: resolvable ? async (action, token) => {
        const raw = (action as { _lsp?: lsp.CodeAction })._lsp;
        const c = [...clientsById.values()].find((x) => x.lang === lang);
        if (!raw || !c || raw.edit) return action;
        const r = await c.request<lsp.CodeAction>("codeAction/resolve", raw, token);
        if (r?.edit) {
          await Promise.all(workspaceEditUris(r.edit).map((u) => ctx.host.ensureModel(m.Uri.parse(u))));
          action.edit = toWorkspaceEdit(r.edit);
        }
        return action;
      } : undefined,
    });
  }
}
