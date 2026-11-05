// <remo-screen-viewer>: ホスト画面のライブ表示と、操作権を持つときのマウス・キーボード・IME 入力の送信
// （docs/architecture/frontend.md §6.2、docs/api/endpoints.md §5）。
// スタイルは Shadow DOM の `css`（constructable stylesheets）だけで、`style` 属性・`<style>` は使わない（CSP。§6.3・§7.1）。
import { LitElement, css, html, nothing, type PropertyValues, type TemplateResult } from "lit";
import { createLogger } from "../lib/logger";
import { MessageTooLargeError, NO_RECONNECT_CLOSE_CODES, WsClient, type WsMessage } from "../lib/ws";
import { isImeKeyEvent, keyModifiers, mouseButtonName, toStreamPoint, wheelNotches, type MouseButtonName, type StreamPoint } from "./input";
import { parseFrame, parseServerMessage, type Lease, type ScreenFrame, type ServerMessage, type Tile } from "./protocol";

const log = createLogger("screen-viewer");

export type ViewerStatus = "connecting" | "open" | "reconnecting" | "failed" | "killswitch";

export interface ViewerErrorDetail {
  code: string;
  message: string;
}

const CLOSE_KILLSWITCH = 4503;

// 閉じコードごとの、failed の理由。どれも、ページの再読み込みを促す文言を添える。
const failureReasons: Record<number, string> = {
  4400: "不正な通信を検出したため、接続を終了しました。",
  4401: "認可に失敗しました。",
  4429: "同時に閲覧できる上限に達しました。",
  4510: "このデバイスは削除または無効化されました。",
};

function overlayText(status: ViewerStatus, closeCode: number): string {
  switch (status) {
    case "connecting":
      return "接続しています…";
    case "reconnecting":
      return "切断されました。再接続しています…";
    case "failed":
      return `${failureReasons[closeCode] ?? "接続できませんでした。"}ページを再読み込みしてください。`;
    case "killswitch":
      return "キルスイッチが有効なため、接続を停止しました。解除後にページを再読み込みしてください。";
    case "open":
      return "";
  }
}

// 接続状態のオーバーレイの先頭に置く印（C4: 状態は色だけにせず、アイコンと文言で伝える）。
// 接続中・再接続中はスピナー（app.css の .spinner と同じ 20px・2px）、失敗・キルスイッチは Iconoir の SVG（atoms/icon.templ と同じ path）。
// Shadow DOM にはページの CSS が届かないので、色は css の var(--danger) などで当てる。
function overlayIcon(status: ViewerStatus): TemplateResult | typeof nothing {
  switch (status) {
    case "connecting":
    case "reconnecting":
      return html`<span class="spinner" aria-hidden="true"></span>`;
    case "failed":
      return html`<svg class="icon tone-danger" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
        <path d="M9.17218 14.8284L12.0006 12M14.829 9.17157L12.0006 12M12.0006 12L9.17218 9.17157M12.0006 12L14.829 14.8284"></path>
        <path d="M12 22C17.5228 22 22 17.5228 22 12C22 6.47715 17.5228 2 12 2C6.47715 2 2 6.47715 2 12C2 17.5228 6.47715 22 12 22Z"></path>
      </svg>`;
    case "killswitch":
      return html`<svg class="icon tone-warning" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
        <path d="M20.0429 21H3.95705C2.41902 21 1.45658 19.3364 2.22324 18.0031L10.2662 4.01533C11.0352 2.67792 12.9648 2.67791 13.7338 4.01532L21.7768 18.0031C22.5434 19.3364 21.581 21 20.0429 21Z"></path>
        <path d="M12 9V13"></path>
        <path d="M12 17.01L12.01 16.9989"></path>
      </svg>`;
    case "open":
      return nothing;
  }
}

export class RemoScreenViewer extends LitElement {
  static properties = {
    wsUrl: { attribute: "ws-url" },
    readonly: { type: Boolean },
    monitor: { type: Number },
    quality: { type: Number },
    fps: { type: Number },
    status: { state: true },
    closeCode: { state: true },
    leaving: { state: true },
  };

  static styles = css`
    :host {
      display: block;
      position: relative;
      min-height: 8rem;
    }
    canvas {
      display: block;
      width: 100%;
      height: auto;
      background: var(--bone);
    }
    /* IME の入力を受ける、見えない入力要素（フォーカスを受ければ IME が動く。表示はしない）。 */
    .ime-sink {
      position: absolute;
      left: 0;
      top: 0;
      width: 1px;
      height: 1px;
      margin: 0;
      padding: 0;
      border: 0;
      opacity: 0;
      resize: none;
      overflow: hidden;
      pointer-events: none;
    }
    /* 出現はフェードイン、退出はフェードアウト（M5）。トークンは :root のカスタムプロパティで、Shadow DOM にも継承される
       （reduced-motion では 100ms になる）。退出は status が open に戻ったとき、data-leaving を付けて終わるまで残す。 */
    .overlay {
      position: absolute;
      inset: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 0.5rem;
      padding: 1rem;
      background: color-mix(in srgb, var(--bone) 85%, transparent);
      color: var(--ink);
      font-size: 0.875rem;
      text-align: center;
      animation: overlay-fade-in var(--motion-fade-in) var(--ease-out) both;
    }
    .overlay[data-leaving] {
      animation: overlay-fade-out var(--motion-fade-out) var(--ease-in) both;
      pointer-events: none;
    }
    @keyframes overlay-fade-in {
      from {
        opacity: 0;
      }
    }
    @keyframes overlay-fade-out {
      to {
        opacity: 0;
      }
    }
    .icon {
      width: 20px;
      height: 20px;
      flex-shrink: 0;
    }
    .tone-danger {
      color: var(--danger);
    }
    .tone-warning {
      color: var(--warning);
    }
    /* スピナー（O7）: app.css の .spinner と同じ 20px・2px。回転は reduced-motion でも止めない。 */
    .spinner {
      width: 20px;
      height: 20px;
      flex-shrink: 0;
      box-sizing: border-box;
      border: 2px solid var(--accent-soft);
      border-top-color: var(--accent);
      border-radius: 9999px;
      animation: overlay-spin 0.8s linear infinite;
    }
    @keyframes overlay-spin {
      to {
        transform: rotate(360deg);
      }
    }
  `;

  // 属性（サーバーが注入する）。monitor / quality / fps は初期値で、接続後はサーバーの ready / config が正。
  declare wsUrl: string;
  declare readonly: boolean;
  declare monitor: number;
  declare quality: number;
  declare fps: number;
  declare status: ViewerStatus;
  declare closeCode: number;
  // status が open に戻ったあと、フェードアウトが終わるまで残すオーバーレイ（null なら残さない）。
  declare leaving: { status: ViewerStatus; text: string } | null;

  private ws: WsClient | null = null;
  private canvas: HTMLCanvasElement | null = null;
  private sink: HTMLTextAreaElement | null = null;

  // 描画。epoch は、接続・解像度・モニターが変わるたびに増え、古い世代の復号待ちフレームを捨てる。
  private epoch = 0;
  private lastSeq = -1;
  private currentMonitor = -1;
  private awaitingKeyframe = true;
  private renderChain: Promise<void> = Promise.resolve();

  // 入力。
  private leaseMine = false;
  private pressedKeys = new Map<string, string>();
  private pressedButtons = new Set<MouseButtonName>();
  private lastPoint: StreamPoint = { x: 0, y: 0 };
  private pendingMove: { clientX: number; clientY: number } | null = null;
  private moveFrame = 0;

  constructor() {
    super();
    this.wsUrl = "";
    this.readonly = false;
    this.monitor = 0;
    this.quality = 0;
    this.fps = 0;
    this.status = "connecting";
    this.closeCode = 0;
    this.leaving = null;
  }

  // 入力が有効: 最後に受信した lease が held かつ mine: true（pending_approval は承認待ちの本人でも readonly 扱い。
  // サーバー側でも入力は常に検査される）。
  // readonly は描画時の操作権の有無を表す初期のヒントにすぎない。HTMX はこのコンポーネントを差し替えないので、
  // 操作権の取得・解放・剥奪で属性は更新されない。入力の可否は WebSocket の lease だけで決める（ready で現在の状態が届く）。
  private get inputEnabled(): boolean {
    return this.leaseMine;
  }

  connectedCallback(): void {
    super.connectedCallback();
    this.connect();
  }

  disconnectedCallback(): void {
    this.disconnect();
    super.disconnectedCallback();
  }

  protected firstUpdated(): void {
    const canvas = this.renderRoot.querySelector("canvas");
    const sink = this.renderRoot.querySelector("textarea");
    if (!canvas || !sink) {
      return;
    }
    this.canvas = canvas;
    this.sink = sink;
    canvas.addEventListener("mousemove", this.onMouseMove);
    canvas.addEventListener("mousedown", this.onMouseDown);
    canvas.addEventListener("wheel", this.onWheel, { passive: false });
    canvas.addEventListener("contextmenu", this.onContextMenu);
    sink.addEventListener("keydown", this.onKeyDown);
    sink.addEventListener("keyup", this.onKeyUp);
    sink.addEventListener("compositionend", this.onCompositionEnd);
    sink.addEventListener("blur", this.onBlur);
  }

  protected willUpdate(changed: PropertyValues<this>): void {
    if (!changed.has("status")) {
      return;
    }
    const previous = changed.get("status");
    if (this.status !== "open") {
      this.leaving = null;
    } else if (previous !== undefined && previous !== "open") {
      this.leaving = { status: previous, text: overlayText(previous, this.closeCode) };
    }
  }

  protected updated(): void {
    const leaving = this.leaving;
    const overlay = this.renderRoot.querySelector(".overlay[data-leaving]");
    if (!leaving || !overlay) {
      return;
    }
    // フェードアウトが終わったら取り除く（アニメーションが無ければすぐ）。
    void Promise.allSettled(overlay.getAnimations().map((a) => a.finished)).then(() => {
      if (this.leaving === leaving) {
        this.leaving = null;
      }
    });
  }

  protected render() {
    const text = overlayText(this.status, this.closeCode);
    const shown = text === "" ? this.leaving : { status: this.status, text };
    return html`
      <canvas width="16" height="9"></canvas>
      <textarea
        class="ime-sink"
        tabindex="-1"
        aria-label="リモート画面への入力"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
      ></textarea>
      ${shown === null
        ? nothing
        : html`<div class="overlay" role="status" ?data-leaving=${text === ""}>${overlayIcon(shown.status)}<span>${shown.text}</span></div>`}
    `;
  }

  // ---- 接続 ----

  private connect(): void {
    this.disconnect();
    this.status = "connecting";
    this.closeCode = 0;
    this.ws = new WsClient(this.wsUrl, {
      onOpen: () => this.onOpen(),
      onMessage: (message) => this.onMessage(message),
      onClose: (code) => this.onClose(code),
      onGiveUp: () => {
        this.status = "failed";
      },
    });
    this.ws.connect();
  }

  private disconnect(): void {
    this.ws?.close();
    this.ws = null;
    this.releaseInput(false);
    this.leaseMine = false;
    this.epoch++;
  }

  private onOpen(): void {
    // 新しい接続はサーバー側で最初からやり直す（連番・キーフレーム）。
    this.epoch++;
    this.lastSeq = -1;
    this.currentMonitor = -1;
    this.awaitingKeyframe = true;
  }

  private onClose(code: number): void {
    this.epoch++;
    // サーバーは切断で web のリースを解放する。押下中の入力もサーバーが解放する。
    this.releaseInput(false);
    this.leaseMine = false;
    this.closeCode = code;
    if (code === CLOSE_KILLSWITCH) {
      this.status = "killswitch";
    } else if (NO_RECONNECT_CLOSE_CODES.has(code)) {
      this.status = "failed";
    } else {
      this.status = "reconnecting";
    }
  }

  private onMessage(message: WsMessage): void {
    if (message.kind === "binary") {
      this.onFrame(message.data);
      return;
    }
    const parsed = parseServerMessage(message.message);
    if (!parsed) {
      log.warn("dropped an unknown message");
      return;
    }
    this.onServerMessage(parsed);
  }

  private onServerMessage(message: ServerMessage): void {
    switch (message.type) {
      case "ready":
        this.monitor = message.monitor;
        this.quality = message.quality;
        this.fps = message.fps;
        this.applyGeometry(message.monitor, message.width, message.height);
        // 再接続の後はリースが失われている。ready のリース状態を反映し、HTMX にパネルを再取得させる。
        this.applyLease(message.lease);
        break;
      case "config":
        this.monitor = message.monitor;
        this.quality = message.quality;
        this.fps = message.fps;
        this.applyGeometry(message.monitor, message.width, message.height);
        break;
      case "lease":
        this.applyLease(message.lease);
        break;
      case "killswitch":
        if (message.active) {
          this.status = "killswitch";
        }
        break;
      case "error":
        this.emit<ViewerErrorDetail>("remo:error", { code: message.code, message: message.message });
        break;
      case "pong":
        break;
    }
  }

  private emit<T>(name: string, detail: T): void {
    this.dispatchEvent(new CustomEvent<T>(name, { detail, bubbles: true, composed: true }));
  }

  private applyLease(lease: Lease): void {
    this.leaseMine = lease.state === "held" && lease.mine;
    this.syncInput();
    this.emit<Lease>("remo:lease-changed", lease);
  }

  // ---- 描画 ----

  // applyGeometry は、モニターか解像度が変わったら canvas を作り直し（サイズの設定で内容も消える）、次のキーフレームまで描画を保留する。
  private applyGeometry(monitor: number, width: number, height: number): void {
    const canvas = this.canvas;
    if (!canvas || width <= 0 || height <= 0) {
      return;
    }
    if (monitor === this.currentMonitor && canvas.width === width && canvas.height === height) {
      return;
    }
    this.currentMonitor = monitor;
    this.resizeCanvas(canvas, width, height);
    this.awaitingKeyframe = true;
  }

  private resizeCanvas(canvas: HTMLCanvasElement, width: number, height: number): void {
    this.epoch++;
    canvas.width = width;
    canvas.height = height;
  }

  private onFrame(data: ArrayBuffer): void {
    const canvas = this.canvas;
    if (!canvas) {
      return;
    }
    let frame: ScreenFrame;
    try {
      frame = parseFrame(data);
    } catch (error) {
      log.warn("dropped a malformed frame", error);
      return;
    }
    // 連番が古い差分フレームは捨てる。キーフレームは全面の置き換えなので常に採用し、連番を引き継ぐ
    // （モニター・品質・FPS を変えると、サーバーは新しいプロファイルの連番を 1 から数え直すため）。
    if (!frame.keyframe && frame.seq <= this.lastSeq) {
      return;
    }
    this.lastSeq = frame.seq;
    // 選択中のモニター以外（切り替え前の飛行中のフレーム）と、キーフレームを待つ間の差分は描画しない。
    if (frame.monitor !== this.currentMonitor) {
      return;
    }
    if (this.awaitingKeyframe && !frame.keyframe) {
      return;
    }
    if (frame.width !== canvas.width || frame.height !== canvas.height) {
      if (!frame.keyframe) {
        this.awaitingKeyframe = true;
        return;
      }
      this.resizeCanvas(canvas, frame.width, frame.height);
    }
    this.awaitingKeyframe = false;
    this.enqueueDraw(canvas, frame);
  }

  // enqueueDraw は、タイルの復号を並行で始め、描画は連番の順（renderChain）に行う。
  private enqueueDraw(canvas: HTMLCanvasElement, frame: ScreenFrame): void {
    const epoch = this.epoch;
    const decoded = decodeTiles(frame.tiles);
    this.renderChain = this.renderChain.then(async () => {
      const bitmaps = await decoded;
      try {
        if (epoch !== this.epoch) {
          return;
        }
        const ctx = canvas.getContext("2d");
        if (!ctx) {
          return;
        }
        if (frame.keyframe) {
          ctx.clearRect(0, 0, canvas.width, canvas.height);
        }
        frame.tiles.forEach((tile, i) => {
          const bitmap = bitmaps[i];
          if (bitmap) {
            ctx.drawImage(bitmap, tile.x, tile.y);
          }
        });
        if (frame.keyframe && (this.status === "connecting" || this.status === "reconnecting")) {
          this.status = "open";
        }
      } finally {
        for (const bitmap of bitmaps) {
          bitmap?.close();
        }
      }
    });
  }

  // ---- 入力 ----

  private sendInput(message: unknown): void {
    try {
      this.ws?.send(message);
    } catch (error) {
      if (!(error instanceof MessageTooLargeError)) {
        throw error;
      }
      this.emit<ViewerErrorDetail>("remo:error", { code: "invalid_argument", message: "送信するデータが大きすぎます。" });
    }
  }

  private streamPoint(clientX: number, clientY: number): StreamPoint | null {
    const canvas = this.canvas;
    return canvas ? toStreamPoint(clientX, clientY, canvas.getBoundingClientRect(), canvas.width, canvas.height) : null;
  }

  // 入力が無効になったとき（リースの喪失）は、押下中の状態を送らずに捨てる（サーバーが解放する）。
  private syncInput(): void {
    if (!this.inputEnabled) {
      this.releaseInput(false);
    }
  }

  // releaseInput は、押下中のキー・ボタンをすべて離し、間引き中の移動を取り消す。send なら up を送る（フォーカスを失ったとき）。
  private releaseInput(send: boolean): void {
    if (this.moveFrame !== 0) {
      cancelAnimationFrame(this.moveFrame);
      this.moveFrame = 0;
    }
    this.pendingMove = null;
    if (send) {
      for (const [code, key] of this.pressedKeys) {
        this.sendInput({ type: "input.key", action: "up", code, key, modifiers: [] });
      }
      for (const button of this.pressedButtons) {
        this.sendInput({ type: "input.mouse", action: "up", x: this.lastPoint.x, y: this.lastPoint.y, button });
      }
    }
    this.pressedKeys.clear();
    this.pressedButtons.clear();
    window.removeEventListener("mouseup", this.onWindowMouseUp);
  }

  private onMouseMove = (event: MouseEvent): void => {
    if (!this.inputEnabled) {
      return;
    }
    this.pendingMove = { clientX: event.clientX, clientY: event.clientY };
    // 1 アニメーションフレームにつき最大 1 回だけ送る。
    if (this.moveFrame === 0) {
      this.moveFrame = requestAnimationFrame(() => {
        this.moveFrame = 0;
        const move = this.pendingMove;
        this.pendingMove = null;
        const point = move && this.inputEnabled ? this.streamPoint(move.clientX, move.clientY) : null;
        if (point) {
          this.lastPoint = point;
          this.sendInput({ type: "input.mouse", action: "move", x: point.x, y: point.y });
        }
      });
    }
  };

  private onMouseDown = (event: MouseEvent): void => {
    if (!this.inputEnabled) {
      return;
    }
    const button = mouseButtonName(event.button);
    const point = this.streamPoint(event.clientX, event.clientY);
    if (!button || !point) {
      return;
    }
    // 既定のフォーカス移動・選択を止め、キー入力と IME を受ける要素にフォーカスを移す。
    event.preventDefault();
    this.sink?.focus({ preventScroll: true });
    this.lastPoint = point;
    this.pressedButtons.add(button);
    // canvas の外で離されても up を送れるよう、window で受ける。
    window.addEventListener("mouseup", this.onWindowMouseUp);
    this.sendInput({ type: "input.mouse", action: "down", x: point.x, y: point.y, button });
  };

  private onWindowMouseUp = (event: MouseEvent): void => {
    const button = mouseButtonName(event.button);
    if (!button || !this.pressedButtons.delete(button)) {
      return;
    }
    const point = this.streamPoint(event.clientX, event.clientY) ?? this.lastPoint;
    this.lastPoint = point;
    this.sendInput({ type: "input.mouse", action: "up", x: point.x, y: point.y, button });
    if (this.pressedButtons.size === 0) {
      window.removeEventListener("mouseup", this.onWindowMouseUp);
    }
  };

  private onWheel = (event: WheelEvent): void => {
    if (!this.inputEnabled) {
      return;
    }
    event.preventDefault();
    const { dx, dy } = wheelNotches(event.deltaMode, event.deltaX, event.deltaY);
    const point = this.streamPoint(event.clientX, event.clientY);
    if (!point || (dx === 0 && dy === 0)) {
      return;
    }
    this.sendInput({ type: "input.mouse", action: "wheel", x: point.x, y: point.y, dx, dy });
  };

  private onContextMenu = (event: MouseEvent): void => {
    if (this.inputEnabled) {
      event.preventDefault();
    }
  };

  // キー入力は、フォーカスを持つ見えない入力要素（this.sink）で受ける。preventDefault もここでだけ行う。
  private onKeyDown = (event: KeyboardEvent): void => {
    if (!this.inputEnabled || isImeKeyEvent(event) || event.code === "") {
      return;
    }
    event.preventDefault();
    this.pressedKeys.set(event.code, event.key);
    this.sendInput({ type: "input.key", action: "down", code: event.code, key: event.key, modifiers: keyModifiers(event) });
  };

  private onKeyUp = (event: KeyboardEvent): void => {
    // down を送ったキーだけ up を送る（IME の確定の Enter の up などは送らない）。
    if (!this.inputEnabled || isImeKeyEvent(event) || !this.pressedKeys.has(event.code)) {
      return;
    }
    event.preventDefault();
    this.pressedKeys.delete(event.code);
    this.sendInput({ type: "input.key", action: "up", code: event.code, key: event.key, modifiers: keyModifiers(event) });
  };

  private onCompositionEnd = (event: CompositionEvent): void => {
    if (this.sink) {
      this.sink.value = "";
    }
    if (this.inputEnabled && event.data !== "") {
      this.sendInput({ type: "input.text", text: event.data });
    }
  };

  // フォーカスを失ったら、押下中のキー・ボタンすべてに up を送る。
  private onBlur = (): void => {
    this.releaseInput(this.inputEnabled);
  };
}

// decodeTiles は、タイルの JPEG を並行して復号する。復号できなかったタイルは null（描画しない。次のキーフレームで直る）。
async function decodeTiles(tiles: Tile[]): Promise<(ImageBitmap | null)[]> {
  const results = await Promise.allSettled(tiles.map((tile) => createImageBitmap(new Blob([tile.jpeg], { type: "image/jpeg" }))));
  return results.map((result) => {
    if (result.status === "fulfilled") {
      return result.value;
    }
    log.warn("failed to decode a tile", result.reason);
    return null;
  });
}

if (!customElements.get("remo-screen-viewer")) {
  customElements.define("remo-screen-viewer", RemoScreenViewer);
}

declare global {
  interface HTMLElementTagNameMap {
    "remo-screen-viewer": RemoScreenViewer;
  }
}
