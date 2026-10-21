// Client for the multiplexed loopback WebSocket bus (Go: internal/stream).
// Envelope: [u8 kind][u32 BE chanId][payload]. See internal/stream/doc.go.
import { BusInfo } from "./bindings/github.com/sid/harness/internal/app/busservice";

export const Kind = { Open: 1, Data: 2, Close: 3, Ack: 4, JSON: 5 } as const;
const WINDOW = 256 * 1024;
const ACK_FLUSH_BYTES = 64 * 1024;
const ACK_FLUSH_MS = 8;

const enc = new TextEncoder();
const dec = new TextDecoder();

function encode(kind: number, chan: number, payload?: Uint8Array): Uint8Array {
  const n = payload?.length ?? 0;
  const out = new Uint8Array(5 + n);
  out[0] = kind;
  new DataView(out.buffer).setUint32(1, chan);
  if (payload) out.set(payload, 5);
  return out;
}

type Unsub = () => void;

export class BusChannel {
  /** payload bytes sent and not yet acked by the peer */
  private inFlight = 0;
  private queue: { kind: number; payload: Uint8Array }[] = [];
  private jsonSubs = new Set<(v: any) => void>();
  private byteSubs = new Set<(b: Uint8Array) => void>();
  private closeSubs = new Set<() => void>();
  private pendingAck = 0;
  private ackTimer: ReturnType<typeof setTimeout> | undefined;
  closed = false;

  /** @internal */
  constructor(
    private bus: Bus,
    readonly id: number,
    readonly name: string,
    readonly local: boolean,
  ) {}

  onJSON<T = unknown>(fn: (v: T) => void): Unsub {
    this.jsonSubs.add(fn);
    return () => this.jsonSubs.delete(fn);
  }
  onBytes(fn: (b: Uint8Array) => void): Unsub {
    this.byteSubs.add(fn);
    return () => this.byteSubs.delete(fn);
  }
  onClose(fn: () => void): Unsub {
    this.closeSubs.add(fn);
    return () => this.closeSubs.delete(fn);
  }

  /** Uint8Array/ArrayBuffer go out as a data frame; anything else as JSON. */
  send(x: Uint8Array | ArrayBuffer | unknown): void {
    if (x instanceof Uint8Array) this.enqueue(Kind.Data, x);
    else if (x instanceof ArrayBuffer) this.enqueue(Kind.Data, new Uint8Array(x));
    else this.sendJSON(x);
  }
  sendBytes(b: Uint8Array): void {
    this.enqueue(Kind.Data, b);
  }
  sendJSON(v: unknown): void {
    this.enqueue(Kind.JSON, enc.encode(JSON.stringify(v)));
  }

  close(): void {
    if (this.closed) return;
    this.bus._send(encode(Kind.Close, this.id));
    this.markClosed();
  }

  private enqueue(kind: number, payload: Uint8Array) {
    if (this.closed) return;
    this.queue.push({ kind, payload });
    this.pump();
  }

  private pump() {
    if (!this.bus._ready) return;
    while (this.queue.length) {
      const m = this.queue[0];
      // An oversize message may go once the window is completely free.
      if (this.inFlight > 0 && this.inFlight + m.payload.length > WINDOW) return;
      this.queue.shift();
      this.inFlight += m.payload.length;
      this.bus._send(encode(m.kind, this.id, m.payload));
    }
  }

  /** @internal */
  _onAck(n: number) {
    this.inFlight = Math.max(0, this.inFlight - n);
    this.pump();
  }

  /** @internal */
  _onFrame(kind: number, payload: Uint8Array) {
    if (kind === Kind.JSON) {
      let v: unknown;
      try {
        v = JSON.parse(dec.decode(payload));
      } catch {
        v = undefined;
      }
      for (const fn of this.jsonSubs) fn(v);
    } else {
      for (const fn of this.byteSubs) fn(payload);
    }
    this.pendingAck += payload.length;
    if (this.pendingAck >= ACK_FLUSH_BYTES) this.flushAck();
    else if (this.pendingAck > 0 && this.ackTimer === undefined) {
      this.ackTimer = setTimeout(() => this.flushAck(), ACK_FLUSH_MS);
    }
  }

  private flushAck() {
    if (this.ackTimer !== undefined) clearTimeout(this.ackTimer);
    this.ackTimer = undefined;
    if (this.pendingAck === 0) return;
    const p = new Uint8Array(4);
    new DataView(p.buffer).setUint32(0, this.pendingAck);
    this.pendingAck = 0;
    this.bus._send(encode(Kind.Ack, this.id, p));
  }

  /** @internal called on (re)connect */
  _reset() {
    this.inFlight = 0;
    this.pendingAck = 0;
    if (this.ackTimer !== undefined) clearTimeout(this.ackTimer);
    this.ackTimer = undefined;
  }

  /** @internal */
  _resume() {
    this.pump();
  }

  /** @internal */
  markClosed() {
    if (this.closed) return;
    this.closed = true;
    this.queue = [];
    if (this.ackTimer !== undefined) clearTimeout(this.ackTimer);
    this.bus._forget(this);
    for (const fn of this.closeSubs) fn();
  }
}

type OpenListener = (ch: BusChannel) => void;

export class Bus {
  private ws: WebSocket | undefined;
  private channels = new Map<number, BusChannel>();
  private nextId = 1; // odd ids are client-initiated
  private openListeners: { prefix: string; fn: OpenListener }[] = [];
  private backoff = 250;
  private stopped = false;
  private connectPromise: Promise<void> | undefined;
  /** @internal true while the socket is open */
  _ready = false;

  /** Connect (idempotent). Resolves once the first connection is open. */
  connect(): Promise<void> {
    if (!this.connectPromise) {
      this.connectPromise = new Promise<void>((resolve) => this.dial(resolve));
    }
    return this.connectPromise;
  }

  /** Open a client-initiated channel. Frames queue until the socket is ready. */
  open(name: string): BusChannel {
    const id = this.nextId;
    this.nextId += 2;
    const ch = new BusChannel(this, id, name, true);
    this.channels.set(id, ch);
    void this.connect();
    if (this._ready) this._send(encode(Kind.Open, id, enc.encode(name)));
    return ch;
  }

  /**
   * Called when Go opens a channel whose name starts with prefix (including
   * channels that already exist when the socket connects/reconnects).
   */
  onOpen(prefix: string, fn: OpenListener): Unsub {
    const l = { prefix, fn };
    this.openListeners.push(l);
    void this.connect();
    for (const ch of this.channels.values()) {
      if (!ch.local && ch.name.startsWith(prefix)) fn(ch);
    }
    return () => {
      this.openListeners = this.openListeners.filter((x) => x !== l);
    };
  }

  close(): void {
    this.stopped = true;
    this.ws?.close();
  }

  /** @internal */
  _send(b: Uint8Array) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(b as Uint8Array<ArrayBuffer>);
  }

  /** @internal */
  _forget(ch: BusChannel) {
    this.channels.delete(ch.id);
  }

  private async dial(first?: () => void) {
    if (this.stopped) return;
    let url: string;
    try {
      const info = await BusInfo();
      url = `ws://127.0.0.1:${info.port}/bus?token=${info.token}`;
    } catch {
      return this.retry(first);
    }
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    ws.onopen = () => {
      this.backoff = 250;
      this._ready = true;
      for (const ch of this.channels.values()) {
        ch._reset();
        if (ch.local) this._send(encode(Kind.Open, ch.id, enc.encode(ch.name)));
      }
      for (const ch of this.channels.values()) if (ch.local) ch._resume();
      first?.();
      first = undefined;
    };
    ws.onmessage = (ev) => this.onMessage(new Uint8Array(ev.data as ArrayBuffer));
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this._ready = false;
      // Server-initiated channels are re-announced on reconnect; keep them
      // (so subscribers survive) but local channels persist and are re-opened.
      this.retry(first);
    };
    ws.onerror = () => ws.close();
  }

  private retry(first?: () => void) {
    if (this.stopped) return;
    const d = this.backoff;
    this.backoff = Math.min(this.backoff * 2, 5000);
    setTimeout(() => this.dial(first), d);
  }

  private onMessage(b: Uint8Array) {
    if (b.length < 5) return;
    const kind = b[0];
    const id = new DataView(b.buffer, b.byteOffset).getUint32(1);
    const payload = b.subarray(5);
    switch (kind) {
      case Kind.Open: {
        if (id % 2 === 1) return;
        let ch = this.channels.get(id);
        if (!ch) {
          ch = new BusChannel(this, id, dec.decode(payload), false);
          this.channels.set(id, ch);
          for (const l of this.openListeners) if (ch.name.startsWith(l.prefix)) l.fn(ch);
        }
        ch._reset();
        break;
      }
      case Kind.Close:
        this.channels.get(id)?.markClosed();
        break;
      case Kind.Ack: {
        if (payload.length >= 4) this.channels.get(id)?._onAck(new DataView(payload.buffer, payload.byteOffset).getUint32(0));
        break;
      }
      case Kind.Data:
      case Kind.JSON:
        this.channels.get(id)?._onFrame(kind, payload);
        break;
    }
  }
}

/** Shared app-wide bus instance. */
export const bus = new Bus();
