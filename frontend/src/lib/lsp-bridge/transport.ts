// vscode-jsonrpc reader/writer over a bus channel `lsp/<key>`: one JSON-RPC message per frame.
import { AbstractMessageReader, AbstractMessageWriter, type DataCallback, type Disposable, type Message } from "vscode-jsonrpc/browser";
import type { BusChannel } from "../bus";

export type LspStatusFrame = { type: "lsp_status"; state: string };
const isStatus = (v: unknown): v is LspStatusFrame => typeof v === "object" && v !== null && (v as { type?: unknown }).type === "lsp_status";

export class BusMessageReader extends AbstractMessageReader {
  private offs: (() => void)[] = [];
  constructor(private ch: BusChannel, private onStatus: (s: LspStatusFrame) => void) {
    super();
    this.offs.push(ch.onClose(() => this.fireClose()));
  }
  listen(callback: DataCallback): Disposable {
    const off = this.ch.onJSON<unknown>((v) => {
      if (isStatus(v)) return this.onStatus(v);
      if (v && typeof v === "object") callback(v as Message);
    });
    this.offs.push(off);
    return { dispose: off };
  }
  dispose(): void {
    super.dispose();
    this.offs.forEach((f) => f());
    this.offs = [];
  }
}

export class BusMessageWriter extends AbstractMessageWriter {
  constructor(private ch: BusChannel) {
    super();
  }
  async write(msg: Message): Promise<void> {
    if (this.ch.closed) {
      const e = new Error("lsp channel closed");
      this.fireError(e, msg, 0);
      throw e;
    }
    this.ch.sendJSON(msg);
  }
  end(): void {}
}
