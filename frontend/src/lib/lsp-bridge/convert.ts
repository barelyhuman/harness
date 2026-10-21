// LSP <-> Monaco type conversions. Pure functions; only `import type` from the protocol package so
// the node build of vscode-jsonrpc is never pulled in.
import type * as lsp from "vscode-languageserver-protocol";
import type * as M from "monaco-editor";
import { ensureMonaco } from "../monaco-setup";

const m = () => ensureMonaco();

export const toPos = (p: M.IPosition): lsp.Position => ({ line: p.lineNumber - 1, character: p.column - 1 });
export const fromPos = (p: lsp.Position): M.IPosition => ({ lineNumber: p.line + 1, column: p.character + 1 });
export const toRange = (r: M.IRange): lsp.Range => ({
  start: { line: r.startLineNumber - 1, character: r.startColumn - 1 },
  end: { line: r.endLineNumber - 1, character: r.endColumn - 1 },
});
export const fromRange = (r: lsp.Range): M.IRange => ({
  startLineNumber: r.start.line + 1,
  startColumn: r.start.character + 1,
  endLineNumber: r.end.line + 1,
  endColumn: r.end.character + 1,
});

/** Monaco file extensions that need a different LSP languageId than the Monaco one. */
export function lspLanguageId(monacoLang: string, path: string): string {
  if (monacoLang === "typescript" && path.endsWith(".tsx")) return "typescriptreact";
  if (monacoLang === "javascript" && path.endsWith(".jsx")) return "javascriptreact";
  return monacoLang;
}

export function markdown(v: string | lsp.MarkupContent | lsp.MarkedString | undefined | null): M.IMarkdownString | undefined {
  if (v == null) return undefined;
  if (typeof v === "string") return { value: v };
  if ("kind" in v) return { value: v.value };
  return { value: "```" + v.language + "\n" + v.value + "\n```" };
}

export function docString(v: string | lsp.MarkupContent | undefined): string | M.IMarkdownString | undefined {
  if (v == null) return undefined;
  if (typeof v === "string") return v;
  return v.kind === "markdown" ? { value: v.value } : v.value;
}

// LSP CompletionItemKind (1..25) -> Monaco
export function completionKind(k: number | undefined): M.languages.CompletionItemKind {
  const K = m().languages.CompletionItemKind;
  const map: Record<number, M.languages.CompletionItemKind> = {
    1: K.Text, 2: K.Method, 3: K.Function, 4: K.Constructor, 5: K.Field, 6: K.Variable, 7: K.Class, 8: K.Interface,
    9: K.Module, 10: K.Property, 11: K.Unit, 12: K.Value, 13: K.Enum, 14: K.Keyword, 15: K.Snippet, 16: K.Color,
    17: K.File, 18: K.Reference, 19: K.Folder, 20: K.EnumMember, 21: K.Constant, 22: K.Struct, 23: K.Event,
    24: K.Operator, 25: K.TypeParameter,
  };
  return (k && map[k]) || K.Property;
}

/** LSP SymbolKind is Monaco's SymbolKind + 1. */
export const symbolKind = (k: number): M.languages.SymbolKind => (k - 1) as M.languages.SymbolKind;

export function severity(s: number | undefined): M.MarkerSeverity {
  const S = m().MarkerSeverity;
  switch (s) {
    case 1: return S.Error;
    case 2: return S.Warning;
    case 3: return S.Info;
    default: return S.Hint;
  }
}

export function toMarker(d: lsp.Diagnostic): M.editor.IMarkerData {
  const T = m().MarkerTag;
  const code = typeof d.code === "object" ? undefined : d.code;
  const r = fromRange(d.range);
  return {
    ...r,
    severity: severity(d.severity),
    message: typeof d.message === "string" ? d.message : d.message.value,
    source: d.source,
    code: code !== undefined ? String(code) : undefined,
    tags: d.tags?.map((t) => (t === 1 ? T.Unnecessary : T.Deprecated)),
  };
}

export type CompletionExtra = { _lsp?: lsp.CompletionItem };

export function toCompletionItem(
  it: lsp.CompletionItem,
  defaults: lsp.CompletionList["itemDefaults"] | undefined,
  wordRange: M.IRange,
  model: M.editor.ITextModel,
): M.languages.CompletionItem & CompletionExtra {
  const R = m().languages.CompletionItemInsertTextRule;
  const label = it.label;
  const fmt = it.insertTextFormat ?? defaults?.insertTextFormat;
  const te = (it.textEdit ?? defaults?.editRange) as lsp.TextEdit | lsp.InsertReplaceEdit | lsp.Range | undefined;
  let range: M.IRange | { insert: M.IRange; replace: M.IRange } = wordRange;
  let text = it.insertText ?? label;
  if (te) {
    const newText = "newText" in te ? te.newText : text;
    text = newText;
    if ("insert" in te && "replace" in te) {
      const ins = fromRange(te.insert), rep = fromRange(te.replace);
      if (ins.startLineNumber === ins.endLineNumber && rep.startLineNumber === rep.endLineNumber) range = { insert: ins, replace: rep };
    } else if ("range" in te) {
      const r = fromRange(te.range);
      if (r.startLineNumber === r.endLineNumber) range = r;
    } else if ("start" in te) {
      const r = fromRange(te as lsp.Range);
      if (r.startLineNumber === r.endLineNumber) range = r;
    }
  }
  void model;
  return {
    label: it.labelDetails ? { label, detail: it.labelDetails.detail, description: it.labelDetails.description } : label,
    kind: completionKind(it.kind),
    detail: it.detail,
    documentation: docString(it.documentation),
    sortText: it.sortText,
    filterText: it.filterText,
    preselect: it.preselect,
    insertText: text,
    insertTextRules: fmt === 2 ? R.InsertAsSnippet : undefined,
    range,
    commitCharacters: it.commitCharacters ?? defaults?.commitCharacters,
    additionalTextEdits: it.additionalTextEdits?.map((e) => ({ range: fromRange(e.range), text: e.newText })),
    tags: it.tags?.includes(1) ? [1 as M.languages.CompletionItemTag] : undefined,
    _lsp: it,
  };
}

export function toTextEdits(edits: lsp.TextEdit[] | null | undefined): M.languages.TextEdit[] {
  return (edits ?? []).map((e) => ({ range: fromRange(e.range), text: e.newText }));
}

export function toLocation(l: lsp.Location | lsp.LocationLink): M.languages.Location | M.languages.LocationLink {
  const Uri = m().Uri;
  if ("targetUri" in l) {
    return {
      uri: Uri.parse(l.targetUri),
      range: fromRange(l.targetRange),
      originSelectionRange: l.originSelectionRange ? fromRange(l.originSelectionRange) : undefined,
      targetSelectionRange: fromRange(l.targetSelectionRange),
    };
  }
  return { uri: Uri.parse(l.uri), range: fromRange(l.range) };
}
export const locationUri = (l: lsp.Location | lsp.LocationLink) => ("targetUri" in l ? l.targetUri : l.uri);

export function toWorkspaceEdit(we: lsp.WorkspaceEdit | null | undefined): M.languages.WorkspaceEdit {
  const Uri = m().Uri;
  const edits: M.languages.IWorkspaceTextEdit[] = [];
  if (we?.documentChanges) {
    for (const dc of we.documentChanges) {
      if ("edits" in dc) {
        for (const e of dc.edits) {
          if ("newText" in e) edits.push({ resource: Uri.parse(dc.textDocument.uri), versionId: undefined, textEdit: { range: fromRange(e.range), text: e.newText } });
        }
      }
    }
  } else if (we?.changes) {
    for (const [uri, es] of Object.entries(we.changes)) {
      for (const e of es) edits.push({ resource: Uri.parse(uri), versionId: undefined, textEdit: { range: fromRange(e.range), text: e.newText } });
    }
  }
  return { edits };
}
export function workspaceEditUris(we: lsp.WorkspaceEdit | null | undefined): string[] {
  const out = new Set<string>();
  we?.documentChanges?.forEach((dc) => "edits" in dc && out.add(dc.textDocument.uri));
  if (we?.changes) Object.keys(we.changes).forEach((u) => out.add(u));
  return [...out];
}
