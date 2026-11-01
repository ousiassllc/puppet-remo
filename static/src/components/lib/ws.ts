// WebSocket のラッパー（docs/architecture/frontend.md §6.4、docs/api/endpoints.md §5）。
// Lit は必ずこれ経由で WebSocket を使い、`new WebSocket()` を直接呼ばない。
import { createLogger } from "./logger";

const log = createLogger("ws");

export const SUBPROTOCOL = "puppet-remo.v1";
// クライアント → サーバーのメッセージの上限（NFR-S09）。超えると、サーバーは 4400 で閉じる。
export const MAX_MESSAGE_BYTES = 64 * 1024;
export const MAX_RECONNECTS = 10;
const BASE_DELAY_MS = 500;
const MAX_DELAY_MS = 15_000;

// 再接続しない閉じコード（4400 不正なメッセージ / 4401 認可失敗 / 4429 閲覧者の上限 / 4503 キルスイッチ / 4510 クライアントの無効化）。
export const NO_RECONNECT_CLOSE_CODES: ReadonlySet<number> = new Set([4400, 4401, 4429, 4503, 4510]);

export type WsMessage = { kind: "text"; message: unknown } | { kind: "binary"; data: ArrayBuffer };

export interface WsCallbacks {
  // 接続できたとき。再接続の後も呼ばれる。
  onOpen?: () => void;
  onMessage?: (message: WsMessage) => void;
  // 接続が閉じるたびに呼ばれる（再接続する場合も）。
  onClose?: (code: number) => void;
  // 再接続の試行が上限に達したとき（最後の onClose の後）。
  onGiveUp?: () => void;
}

export class MessageTooLargeError extends Error {
  constructor(readonly bytes: number) {
    super(`message is ${bytes} bytes (limit ${MAX_MESSAGE_BYTES})`);
    this.name = "MessageTooLargeError";
  }
}

// reconnectDelay は、attempt 回目（1 始まり）の再接続までの待ち時間（ミリ秒）: 500 ms から倍々で、上限 15 s。
export function reconnectDelay(attempt: number): number {
  return Math.min(BASE_DELAY_MS * 2 ** (attempt - 1), MAX_DELAY_MS);
}

const encoder = new TextEncoder();

export class WsClient {
  private socket: WebSocket | null = null;
  private timer: number | null = null;
  private attempts = 0;
  private stopped = false;
  private healthy = false;

  constructor(
    private readonly url: string,
    private readonly callbacks: WsCallbacks,
  ) {}

  connect(): void {
    this.stopped = false;
    this.open();
  }

  // send は、開いている接続へ JSON で送る。接続が開いていなければ false（入力は捨てる）。
  // 64 KiB を超えるメッセージは送らず MessageTooLargeError を投げる。
  send(message: unknown): boolean {
    const json = JSON.stringify(message);
    const bytes = encoder.encode(json).length;
    if (bytes > MAX_MESSAGE_BYTES) {
      throw new MessageTooLargeError(bytes);
    }
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      return false;
    }
    this.socket.send(json);
    return true;
  }

  // close は、接続とタイマーを破棄する。以降コールバックは呼ばれない。
  close(): void {
    this.stopped = true;
    if (this.timer !== null) {
      window.clearTimeout(this.timer);
      this.timer = null;
    }
    const socket = this.socket;
    this.socket = null;
    socket?.close(1000);
  }

  private open(): void {
    this.timer = null;
    let socket: WebSocket;
    try {
      socket = new WebSocket(this.url, SUBPROTOCOL);
    } catch (error) {
      log.error("cannot create a WebSocket", error);
      this.stopped = true;
      this.callbacks.onGiveUp?.();
      return;
    }
    socket.binaryType = "arraybuffer";
    this.socket = socket;
    this.healthy = false;
    socket.onopen = () => {
      if (this.socket === socket) {
        this.callbacks.onOpen?.();
      }
    };
    socket.onmessage = (event: MessageEvent) => {
      if (this.socket === socket) {
        this.receive(event.data);
      }
    };
    socket.onerror = () => {
      log.debug("socket error");
    };
    socket.onclose = (event: CloseEvent) => {
      if (this.socket === socket) {
        this.socket = null;
        this.closed(event.code);
      }
    };
  }

  private receive(data: unknown): void {
    // サーバーから 1 つでも受け取れた接続は健全とみなし、連続の失敗の数え直しをする（接続して直ぐ閉じる繰り返しを数えるため）。
    if (!this.healthy) {
      this.healthy = true;
      this.attempts = 0;
    }
    if (data instanceof ArrayBuffer) {
      this.callbacks.onMessage?.({ kind: "binary", data });
      return;
    }
    if (typeof data !== "string") {
      return;
    }
    let message: unknown;
    try {
      message = JSON.parse(data);
    } catch {
      log.warn("dropped a text frame that is not JSON");
      return;
    }
    this.callbacks.onMessage?.({ kind: "text", message });
  }

  private closed(code: number): void {
    log.debug("closed", code);
    this.callbacks.onClose?.(code);
    if (this.stopped) {
      return;
    }
    if (NO_RECONNECT_CLOSE_CODES.has(code)) {
      this.stopped = true;
      return;
    }
    this.attempts++;
    if (this.attempts > MAX_RECONNECTS) {
      this.stopped = true;
      this.callbacks.onGiveUp?.();
      return;
    }
    this.timer = window.setTimeout(() => this.open(), reconnectDelay(this.attempts));
  }
}
