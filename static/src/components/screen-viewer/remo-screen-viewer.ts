// <remo-screen-viewer>: ホスト画面のライブ表示と、操作権を持つときのマウス・キーボード・IME 入力の送信
// （docs/architecture/frontend.md §6.2、docs/api/endpoints.md §5）。
// スタイルは Shadow DOM の `css`（constructable stylesheets）だけで、`style` 属性・`<style>` は使わない（CSP。§6.3・§7.1）。
import { LitElement, css, html, nothing, type PropertyValues, type TemplateResult } from "lit";
import { createLogger } from "../lib/logger";
import { MessageTooLargeError, NO_RECONNECT_CLOSE_CODES, WsClient, type WsMessage } from "../lib/ws";
import { isImeKeyEvent, keyModifiers, mouseButtonName, toStreamPoint, wheelNotches, type MouseButtonName, type StreamPoint } from "./input";
import { GROUP_MONITOR, parseFrame, parseServerMessage, type Lease, type ScreenFrame, type ServerMessage, type StreamGeometry, type StreamSource, type TargetState, type Tile } from "./protocol";

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
    target: { state: true },
    source: { state: true },
    leaving: { state: true },
  };

  static styles = css`
    :host {
      display: block;
      position: relative;
      min-height: 8rem;
    }
    /* キー入力を受けている間（IME 用の入力要素にフォーカスがある間）は、ホストの外側に --focus のリング（C3）。
       入力要素は見えず、Shadow DOM の中なので、グローバルの :focus-visible は届かない。 */
    :host(:focus-within) {
      outline: 2px solid var(--focus);
      outline-offset: 2px;
    }
    canvas {
      display: block;
      width: 100%;
      height: auto;
      background: var(--bone);
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
    .sourcebar {
      /* 「対象を表示できません」の覆いの上に出す（表示できないときこそ、モニター表示へ切り替えられるように）。 */
      position: relative;
      z-index: 1;
      display: flex;
      justify-content: flex-end;
      padding: 0.25rem 0;
    }
    .sourcebar button,
    .banner button {
      border: 1px solid var(--border);
      border-radius: var(--r-md);
      background: transparent;
      color: var(--ink);
      padding: 0.25rem 0.75rem;
      font: inherit;
      font-size: 0.875rem;
      cursor: pointer;
    }
    .sourcebar button:hover,
    .banner button:hover {
      background: var(--accent-soft);
    }
    .sourcebar button:focus-visible,
    .banner button:focus-visible {
      outline: 2px solid var(--focus);
      outline-offset: 2px;
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
      color: var(--ink);
    }
    /* 状態色の枠（40%）と地（10%）はガイド C4 の式（in srgb）。Shadow DOM には app.css の -line・-wash が届かないので、カスタムプロパティから混色する。 */
    .banner.halted {
      border: 1px solid color-mix(in srgb, var(--danger) 40%, transparent);
      background: color-mix(in srgb, var(--danger) 10%, transparent);
    }
    .banner.notice {
      border: 1px solid color-mix(in srgb, var(--warning) 40%, transparent);
      background: color-mix(in srgb, var(--warning) 10%, transparent);
    }
    .banner .text {
      flex: 1 1 18rem;
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
    /* スピナー（O7）: app.css の .spinner と同じ 20px・2px・--spinner-duration（:root のトークンで Shadow DOM にも継承される）。
       reduced-motion では回転を止める。forced-colors ではトラックを透明にし、先端の円弧だけを CanvasText で残す。 */
    .spinner {
      width: 20px;
      height: 20px;
      flex-shrink: 0;
      box-sizing: border-box;
      border: 2px solid var(--accent-soft);
      border-top-color: var(--accent);
      border-radius: var(--r-full);
      animation: overlay-spin var(--spinner-duration) linear infinite;
    }
    @media (prefers-reduced-motion: reduce) {
      .spinner {
        animation: none;
      }
    }
    @media (forced-colors: active) {
      .spinner {
        border-color: transparent;
        border-top-color: CanvasText;
      }
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
  declare target: TargetState | null;
  // 配信の源（ready・config の source。#157）。表示の切り替えボタンが読む。
  declare source: StreamSource;
  // status が open に戻ったあと、フェードアウトが終わるまで残すオーバーレイ（null なら残さない）。
  declare leaving: { status: ViewerStatus; text: string } | null;

  private ws: WsClient | null = null;
  private canvas: HTMLCanvasElement | null = null;
  private sink: HTMLTextAreaElement | null = null;

  // 描画。epoch は、接続・解像度・モニターが変わるたびに増え、古い世代の復号待ちフレームを捨てる。
  private epoch = 0;
  private lastSeq = -1;
  private currentMonitor = -1;
  // グループ源で今 canvas に出している画像の原点・大きさ（#157）。入力の座標はこの画像の座標なので、
  // マウスのメッセージに添えて送る（サーバーが今の画像と違えば、古い座標として注入しない）。画像が来るまで・モニター源は null。
  private groupRef: { ox: number; oy: number; w: number; h: number } | null = null;
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
    this.source = "monitor";
    this.leaving = null;
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
    let text = overlayText(this.status, this.closeCode);
    // 対象を撮れないときは、画像が来ない（接続中のまま）。再接続・失敗・キルスイッチの表示を優先し、それ以外は「表示できません」にする。
    // 停止は「再開」を押すまで続く（原因が消えて画像が撮れるようになっても、自動では再開しない）ので、覆いも再開まで出す。
    // モニター表示に切り替えているあいだは、モニターの画像が流れているので出さない。
    const cannotDisplay = this.source === "group" && this.target?.state === "halted" && cannotDisplayReasons.has(this.target.reason ?? "");
    if (cannotDisplay && (this.status === "open" || this.status === "connecting")) {
      text = "対象を表示できません。画面全体の表示には切り替えません。停止は、原因を取り除いて「再開」を押すまで続きます。";
    }
    const banner = targetBanner(this.target);
    const shown = text === "" ? this.leaving : { status: this.status, text };
    return html`
      ${this.renderSourceBar()}
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
      ${banner === null ? nothing : this.renderBanner(banner)}
    `;
  }

  // 表示の切り替え（#157。D1）: スコープ付きのリースを持つあいだ、モニター表示（画面全体）と、選んだウィンドウの表示を行き来できる。
  // スコープ付きのリースは切り替えても保たれる。ウィンドウ表示への切り替えは、このビューアが今もそのリースを持つときだけサーバーが受ける。
  private renderSourceBar() {
    if (this.target === null) {
      return nothing;
    }
    return html`
      <div class="sourcebar" data-source-bar>
        ${this.source === "group"
          ? html`<button type="button" data-source-monitor @click=${this.onShowMonitor}>モニター表示</button>`
          : html`<button type="button" data-source-group @click=${this.onShowWindow}>ウィンドウ表示（選択中の対象に戻る）</button>`}
      </div>
    `;
  }

  private onShowMonitor = (): void => {
    this.sendInput({ type: "config", source: "monitor" });
  };

  private onShowWindow = (): void => {
    this.sendInput({ type: "config", source: "group" });
  };

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
    this.source = "monitor";
    this.groupRef = null;
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
  //
  // グループ源のまま大きさだけが変わるとき（メニュー・ダイアログの開閉）は、canvas を今は作り直さない（作り直すと内容が消えてちらつく）。
  // 次のキーフレーム（サーバーは大きさ・原点が変わると必ずキーフレームにする）が来たときに、onFrame が作り直す。それまでの入力は、古い画像の
  // 原点・大きさ（groupRef）を添えて送るので、サーバーが古い座標として拒否する（stale_frame）。
  private applyGeometry(geometry: StreamGeometry): void {
    const { width, height } = geometry;
    const monitor = geometry.source === "group" ? GROUP_MONITOR : geometry.monitor;
    const canvas = this.canvas;
    this.source = geometry.source;
    if (geometry.source !== "group" || width <= 0 || height <= 0 || geometry.origin === undefined) {
      this.groupRef = null; // モニター源、または最初のコマの前（画像が来るまでグループ源の入力は送らない）
    }
    if (!canvas || width <= 0 || height <= 0) {
      return;
    }
    if (monitor === this.currentMonitor && canvas.width === width && canvas.height === height) {
      return;
    }
    if (monitor === this.currentMonitor && geometry.source === "group") {
      this.awaitingKeyframe = true; // 大きさの変更は、次のキーフレームで反映する（上のコメント）
      return;
    }
    this.currentMonitor = monitor;
    canvas.toggleAttribute("data-group", monitor === GROUP_MONITOR);
    this.resizeCanvas(canvas, width, height);
    this.groupRef = geometry.source === "group" && geometry.origin !== undefined ? { ox: geometry.origin.x, oy: geometry.origin.y, w: width, h: height } : null;
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
    if (frame.group && frame.origin !== null) {
      this.groupRef = { ox: frame.origin.x, oy: frame.origin.y, w: frame.width, h: frame.height };
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
    if (this.source === "group" && this.groupRef === null) {
      return null; // 対象の画像が来るまで、座標を送らない
    }
    return canvas ? toStreamPoint(clientX, clientY, canvas.getBoundingClientRect(), canvas.width, canvas.height) : null;
  }

  // sendMouse はマウスの入力を送る。グループ源では、座標を読んだ画像の原点・大きさ（ox・oy・w・h）を添える。
  private sendMouse(message: Record<string, unknown>): void {
    this.sendInput(this.source === "group" && this.groupRef !== null ? { ...message, ...this.groupRef } : message);
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
        this.sendMouse({ type: "input.mouse", action: "up", x: this.lastPoint.x, y: this.lastPoint.y, button });
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
    // グループ源では、ボタンを押していないあいだのホバーの move は送らない: 画像の外（対象の外）の座標の move は、サーバーでは
    // 停止の理由になる（ホバーでリース全体が止まらないように）。down / up は座標を持つので、クリックは従来どおり入る。ドラッグ（押下中）は送る。
    if (this.source === "group" && this.pressedButtons.size === 0) {
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
          this.sendMouse({ type: "input.mouse", action: "move", x: point.x, y: point.y });
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
    this.sendMouse({ type: "input.mouse", action: "down", x: point.x, y: point.y, button });
  };

  private onWindowMouseUp = (event: MouseEvent): void => {
    const button = mouseButtonName(event.button);
    if (!button || !this.pressedButtons.delete(button)) {
      return;
    }
    const point = this.streamPoint(event.clientX, event.clientY) ?? this.lastPoint;
    this.lastPoint = point;
    this.pendingMove = null; // down と同じ理由
    this.sendMouse({ type: "input.mouse", action: "up", x: point.x, y: point.y, button });
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
    this.sendMouse({ type: "input.mouse", action: "wheel", x: point.x, y: point.y, dx, dy });
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
