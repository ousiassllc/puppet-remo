// <remo-screen-viewer>: ホスト画面のライブ表示と、操作権を持つときのマウス・キーボード・IME 入力の送信
// （docs/architecture/frontend.md §6.2、docs/api/endpoints.md §5）。
// スタイルは Shadow DOM の `css`（constructable stylesheets）だけで、`style` 属性・`<style>` は使わない（CSP。§6.3・§7.1）。
import { LitElement, css, html, nothing } from "lit";
import { createLogger } from "../lib/logger";
import { MessageTooLargeError, NO_RECONNECT_CLOSE_CODES, WsClient, type WsMessage } from "../lib/ws";
import { isImeKeyEvent, keyModifiers, mouseButtonName, toStreamPoint, wheelNotches, type MouseButtonName, type StreamPoint } from "./input";
import { GROUP_MONITOR, parseFrame, parseServerMessage, type Lease, type ScreenFrame, type ServerMessage, type StreamGeometry, type TargetState, type Tile } from "./protocol";

const log = createLogger("screen-viewer");

export type ViewerStatus = "connecting" | "open" | "reconnecting" | "failed" | "killswitch";

export interface ViewerErrorDetail {
  code: string;
  message: string;
  reason?: string;
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

// 対象ウィンドウ（スコープ付きリース。#157）の停止の理由ごとの文言。停止中はサーバーも入力を受け付けない。
const haltReasons: Record<string, string> = {
  related_unverifiable: "関連するウィンドウ（メニューなど）の所有関係を確認できないため、入力を停止しました。",
  related_cross_process: "別のアプリのダイアログが前面にあるため、入力を停止しました。",
  unrelated_same_process: "同じアプリの別のウィンドウが前面にあるため、入力を停止しました。",
  capture_unavailable: "対象のウィンドウを表示できないため、入力を停止しました。",
  capture_member_unavailable: "関連するメニュー・ダイアログを表示できないため、入力を停止しました。",
  target_not_foreground: "対象と関係のないウィンドウが前面にあるため、入力を停止しました。",
  target_obscured: "対象に重なった別のウィンドウの上の操作だったため、入力を停止しました。",
  outside_target_group: "対象のウィンドウの外の操作だったため、入力を停止しました。",
  own_window: "puppet-remo 自身のウィンドウが対象になったため、入力を停止しました。",
  elevated_window: "管理者権限のウィンドウが対象になったため、入力を停止しました。",
  target_minimized: "対象のウィンドウが最小化されているため、入力を停止しました。",
  target_closed: "対象のウィンドウが閉じられました。",
  target_replaced: "対象のウィンドウが別のウィンドウに置き換わりました。",
  target_unsupported: "このウィンドウは対象にできません。",
};

// 表示できないことを示す停止の理由（モニター全体の表示には切り替えない）。
const cannotDisplayReasons = new Set(["capture_unavailable", "capture_member_unavailable"]);

const CAPTURE_PARTIAL = "capture_partial";

export interface TargetBanner {
  tone: "halted" | "notice";
  text: string;
  resumable: boolean; // 「再開」を出す（選び直しのみの理由では出さない）
}

// targetBanner は target の状態のバナー。halted は停止（入力を送らない）、capture_partial だけの active は通知（停止ではない）。
export function targetBanner(target: TargetState | null): TargetBanner | null {
  if (!target || target.state === "none") {
    return null;
  }
  if (target.state === "halted") {
    const reason = target.reason ?? "";
    const base = haltReasons[reason] ?? `対象の操作を停止しました（理由: ${reason || "不明"}）。`;
    const text = target.reselect
      ? `${base}別のウィンドウを選んでください。`
      : `${base}ホスト側で原因を取り除いてから「再開」を押してください（ブラウザからのキー・クリックでは戻りません）。`;
    return { tone: "halted", text, resumable: !target.reselect };
  }
  if (target.notices.includes(CAPTURE_PARTIAL)) {
    return {
      tone: "notice",
      text: "対象の表示から除外しているウィンドウがあります（所有関係を確認できないメニューなど）。そのウィンドウが前面になると入力を停止します。",
      resumable: false,
    };
  }
  return null;
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
    target: { state: true },
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
      background: #000;
    }
    /* グループ源（#157）は等倍で出す（幅いっぱいに拡大しない。メニュー・ダイアログの開閉で画像の大きさが変わっても、T の表示の大きさ・位置が動かない。幅を超えるときだけ縮める）。 */
    canvas[data-group] {
      width: auto;
      max-width: 100%;
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
    .banner {
      /* 「対象を表示できません」の覆い（.overlay。ホスト全体）の上に出す（「再開」「別のウィンドウを選ぶ」を押せるように）。 */
      position: relative;
      z-index: 1;
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 0.5rem 1rem;
      padding: 0.5rem 0.75rem;
      font-size: 0.875rem;
    }
    .banner.halted {
      background: #fef2f2;
      color: #991b1b;
    }
    .banner.notice {
      background: #fffbeb;
      color: #92400e;
    }
    .banner .text {
      flex: 1 1 18rem;
    }
    .banner button {
      border: 1px solid currentColor;
      border-radius: 0.375rem;
      background: #fff;
      color: inherit;
      padding: 0.25rem 0.75rem;
      font: inherit;
      cursor: pointer;
    }
    .overlay {
      position: absolute;
      inset: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 1rem;
      background: rgb(15 23 42 / 0.75);
      color: #fff;
      font-size: 0.875rem;
      text-align: center;
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
  declare target: TargetState | null;

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
    this.target = null;
  }

  // 入力が有効: 最後に受信した lease が held かつ mine: true（pending_approval は承認待ちの本人でも readonly 扱い。
  // サーバー側でも入力は常に検査される）。
  // readonly は描画時の操作権の有無を表す初期のヒントにすぎない。HTMX はこのコンポーネントを差し替えないので、
  // 操作権の取得・解放・剥奪で属性は更新されない。入力の可否は WebSocket の lease だけで決める（ready で現在の状態が届く）。
  // 対象ウィンドウの操作が停止（halted。#157）している間は、Esc を含む一切の入力を送らない（サーバーも受け付けない）。
  // 復帰は、ホスト側で原因を取り除いたうえでの明示的な「再開」だけ。
  private get inputEnabled(): boolean {
    return this.leaseMine && this.target?.state !== "halted";
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

  protected render() {
    let text = overlayText(this.status, this.closeCode);
    // 対象を撮れないときは、画像が来ない（接続中のまま）。再接続・失敗・キルスイッチの表示を優先し、それ以外は「表示できません」にする。
    const cannotDisplay = this.target?.state === "halted" && cannotDisplayReasons.has(this.target.reason ?? "");
    if (cannotDisplay && (this.status === "open" || this.status === "connecting")) {
      text = "対象を表示できません。画面全体の表示には切り替えません。";
    }
    const banner = targetBanner(this.target);
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
      ${text === "" ? nothing : html`<div class="overlay" role="status">${text}</div>`}
      ${banner === null ? nothing : this.renderBanner(banner)}
    `;
  }

  private renderBanner(banner: TargetBanner) {
    return html`
      <div class="banner ${banner.tone}" role=${banner.tone === "halted" ? "alert" : "status"} data-target-banner data-target-tone=${banner.tone}>
        <span class="text">${banner.text}</span>
        ${banner.tone === "halted"
          ? html`
              ${banner.resumable ? html`<button type="button" data-target-resume @click=${this.onResume}>再開</button>` : nothing}
              <button type="button" data-target-reselect @click=${this.onReselect}>別のウィンドウを選ぶ</button>
            `
          : nothing}
      </div>
    `;
  }

  // 再開は、サーバーに全条件の再検証を求める（Control.ResumeTarget）。通れば target: active、通らなければ halted のまま理由が更新される。
  // 自動では再開しない。
  private onResume = (): void => {
    this.sendInput({ type: "target.resume" });
  };

  // 「別のウィンドウを選ぶ」は、ページ側（HTMX）がリースを解放して一覧を出す。
  private onReselect = (): void => {
    this.emit("remo:reselect", {});
  };

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
    this.target = null;
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
    this.target = null;
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
        this.applyGeometry(message);
        // 再接続の後はリースが失われている。ready のリース状態を反映し、HTMX にパネルを再取得させる。
        this.applyLease(message.lease);
        break;
      case "config":
        this.monitor = message.monitor;
        this.quality = message.quality;
        this.fps = message.fps;
        this.applyGeometry(message);
        break;
      case "target":
        this.applyTarget(message.target);
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
        if (this.explainedByTargetBanner(message.code, message.reason)) {
          break;
        }
        this.emit<ViewerErrorDetail>("remo:error", message.reason === undefined ? { code: message.code, message: message.message } : { code: message.code, message: message.message, reason: message.reason });
        break;
      case "pong":
        break;
    }
  }

  // スコープ付きのリースの最中の、停止につながる入力の拒否は、続く target メッセージの停止バナーが理由を示す（トーストにしない）。
  // スコープの無い入力の拒否（自ウィンドウ・昇格など）は、バナーが出ないのでトーストにする。
  private explainedByTargetBanner(code: string, reason: string | undefined): boolean {
    if (code !== "input_blocked" || reason === undefined || this.target === null) {
      return false;
    }
    // 停止の直前に送った入力（key up など）の拒否（target_halted）も、停止のバナーが出ていれば重ねて出さない。
    return (Object.hasOwn(haltReasons, reason) && this.target.state !== "none") || (reason === "target_halted" && this.target.state === "halted");
  }

  private emit<T>(name: string, detail: T): void {
    this.dispatchEvent(new CustomEvent<T>(name, { detail, bubbles: true, composed: true }));
  }

  private applyLease(lease: Lease): void {
    this.leaseMine = lease.state === "held" && lease.mine;
    this.syncInput();
    this.emit<Lease>("remo:lease-changed", lease);
  }

  // applyTarget は target メッセージを反映する。halted になったら、押下中の入力を送らずに捨てる（サーバーが ReleaseAll で解放する）。
  private applyTarget(target: TargetState): void {
    this.target = target.state === "none" ? null : target;
    this.syncInput();
  }

  // ---- 描画 ----

  // applyGeometry は、配信の源（モニター・グループ）か解像度が変わったら canvas を作り直し（サイズの設定で内容も消える）、
  // 次のキーフレームまで描画を保留する。グループ源の width / height は合成画像の大きさで、メニュー・ダイアログの開閉で変わる。
  // 入力の座標は canvas の座標（合成画像内の座標。サーバーが原点を足して物理座標にする）なので、大きさが変われば変換も追従する。
  private applyGeometry(geometry: StreamGeometry): void {
    const { width, height } = geometry;
    const monitor = geometry.source === "group" ? GROUP_MONITOR : geometry.monitor;
    const canvas = this.canvas;
    if (!canvas || width <= 0 || height <= 0) {
      return;
    }
    if (monitor === this.currentMonitor && canvas.width === width && canvas.height === height) {
      return;
    }
    this.currentMonitor = monitor;
    canvas.toggleAttribute("data-group", monitor === GROUP_MONITOR);
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
    // 次のフレームへ持ち越した移動は捨てる（down が同じ位置へ動かす。メニューを閉じるクリックの直後に、古い画像の座標の移動が
    // 対象の外の座標として届き、停止になるのを避ける）。
    this.pendingMove = null;
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
    this.pendingMove = null; // down と同じ理由
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
