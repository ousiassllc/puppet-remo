// テスト用の偽の WebSocket。`lib/ws.ts` は `new WebSocket()` と `WebSocket.OPEN` をグローバルから引くので、
// installFakeWebSocket() が window.WebSocket を差し替える。サーバー側の動き（接続の成立・メッセージ・閉じる）はテストが駆動する。

export class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  // 作られた接続（作られた順）。
  static instances: FakeWebSocket[] = [];
  // true の間、コンストラクターが例外を投げる（不正な URL・CSP による拒否の再現）。
  static throwOnConstruct = false;

  static get last(): FakeWebSocket {
    const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    if (!socket) {
      throw new Error("no FakeWebSocket has been created");
    }
    return socket;
  }

  readonly url: string;
  readonly protocols: string | string[] | undefined;
  binaryType: BinaryType = "blob";
  readyState: number = FakeWebSocket.CONNECTING;
  // クライアントが送ったテキスト。
  readonly sent: string[] = [];
  // クライアントが close() に渡したコード（呼ばれていなければ undefined）。
  closedByClient: number | undefined;

  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;

  constructor(url: string, protocols?: string | string[]) {
    if (FakeWebSocket.throwOnConstruct) {
      throw new DOMException("blocked", "SecurityError");
    }
    this.url = url;
    this.protocols = protocols;
    FakeWebSocket.instances.push(this);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  // クライアントからの close。実物と違い close イベントは発火しない（サーバー側の動きは serverClose で駆動する）。
  close(code?: number): void {
    this.closedByClient = code;
    this.readyState = FakeWebSocket.CLOSED;
  }

  // 送られた JSON のメッセージ。
  get sentMessages(): Record<string, unknown>[] {
    return this.sent.map((text) => JSON.parse(text) as Record<string, unknown>);
  }

  // ---- サーバー側の駆動 ----

  open(): void {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.(new Event("open"));
  }

  // receive は、テキスト（string）かバイナリ（ArrayBuffer）のメッセージを届ける。
  receive(data: string | ArrayBuffer): void {
    this.onmessage?.(new MessageEvent("message", { data }));
  }

  receiveJson(message: unknown): void {
    this.receive(JSON.stringify(message));
  }

  serverClose(code: number): void {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.(new CloseEvent("close", { code }));
  }
}

// installFakeWebSocket は、window.WebSocket を偽物にする。戻り値は元に戻す関数。
export function installFakeWebSocket(): () => void {
  const original = window.WebSocket;
  FakeWebSocket.instances = [];
  FakeWebSocket.throwOnConstruct = false;
  window.WebSocket = FakeWebSocket as unknown as typeof WebSocket;
  return () => {
    window.WebSocket = original;
    FakeWebSocket.instances = [];
    FakeWebSocket.throwOnConstruct = false;
  };
}
