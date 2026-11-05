// <remo-screen-viewer> のテスト（docs/architecture/frontend.md §6.2・§9、docs/api/endpoints.md §5）。
// 偽の WebSocket がサーバーの代わりにメッセージとバイナリフレーム（実物の JPEG のタイル）を流し、
// canvas の画素・送られた入力・発火するイベント・Shadow DOM の表示を確かめる。
import { fixture, html, expect } from "@open-wc/testing";
import { FakeTimers } from "../testing/fake-timers";
import { FakeWebSocket, installFakeWebSocket } from "../testing/fake-websocket";
import { buildFrame, classifyPixel, solidTile, until, type ColorName, type TestFrame } from "../testing/frames";
import { MAX_RECONNECTS } from "../lib/ws";
import type { Lease } from "./protocol";
import "./remo-screen-viewer";
import type { RemoScreenViewer, ViewerErrorDetail } from "./remo-screen-viewer";

const WS_URL = "wss://remo.example.test/ws/screen";
const WIDTH = 64;
const HEIGHT = 48;
const DISPLAY_WIDTH = 320; // canvas の表示幅（ストリームの 5 倍。高さは縦横比で 240）

const heldByMe: Lease = { state: "held", holder: { kind: "web", label: "テスト端末" }, mine: true };
const heldByOther: Lease = { state: "held", holder: { kind: "mcp", label: "AI" }, mine: false };
const idle: Lease = { state: "idle", mine: false };

interface Harness {
  viewer: RemoScreenViewer;
  socket: FakeWebSocket;
  canvas: HTMLCanvasElement;
  sink: HTMLTextAreaElement;
}

// connect は、ビューアを置き、偽のサーバーが接続を受けて ready を返すところまで進める。
async function connect(options: { readonly?: boolean; lease?: Lease } = {}): Promise<Harness> {
  const viewer = await fixture<RemoScreenViewer>(
    options.readonly
      ? html`<remo-screen-viewer ws-url=${WS_URL} readonly></remo-screen-viewer>`
      : html`<remo-screen-viewer ws-url=${WS_URL}></remo-screen-viewer>`,
  );
  viewer.style.width = `${DISPLAY_WIDTH}px`;
  const socket = FakeWebSocket.last;
  socket.open();
  socket.receiveJson({ type: "ready", monitor: 0, width: WIDTH, height: HEIGHT, quality: 60, fps: 10, lease: options.lease ?? idle });
  await viewer.updateComplete;
  const canvas = viewer.shadowRoot?.querySelector("canvas");
  const sink = viewer.shadowRoot?.querySelector("textarea");
  if (!canvas || !sink) {
    throw new Error("the viewer did not render a canvas and a textarea");
  }
  return { viewer, socket, canvas, sink };
}

async function frame(parts: Omit<TestFrame, "tiles"> & { tiles: [ColorName, number, number, number, number][] }): Promise<ArrayBuffer> {
  const tiles = await Promise.all(parts.tiles.map(([color, x, y, w, h]) => solidTile(color, x, y, w, h)));
  return buildFrame({ ...parts, tiles });
}

// 全面を 1 色で覆うキーフレーム。
function fullKeyframe(seq: number, color: ColorName, monitor = 0): Promise<ArrayBuffer> {
  return frame({ keyframe: true, monitor, seq, width: WIDTH, height: HEIGHT, tiles: [[color, 0, 0, WIDTH, HEIGHT]] });
}

function diffFrame(seq: number, color: ColorName, x: number, y: number, monitor = 0): Promise<ArrayBuffer> {
  return frame({ keyframe: false, monitor, seq, width: WIDTH, height: HEIGHT, tiles: [[color, x, y, 16, 16]] });
}

function untilPixel(canvas: HTMLCanvasElement, x: number, y: number, expected: ColorName | "transparent"): Promise<void> {
  return until(() => classifyPixel(canvas, x, y) === expected, `pixel (${x}, ${y}) is ${expected}`);
}

function overlayText(viewer: RemoScreenViewer): string | null {
  return viewer.shadowRoot?.querySelector(".overlay")?.textContent?.trim() ?? null;
}

// ビューアの座標の変換（仕様: round((clientX − rect.left) × canvas.width ÷ rect.width)）。
function expectedPoint(canvas: HTMLCanvasElement, clientX: number, clientY: number): { x: number; y: number } {
  const rect = canvas.getBoundingClientRect();
  return {
    x: Math.round(((clientX - rect.left) * canvas.width) / rect.width),
    y: Math.round(((clientY - rect.top) * canvas.height) / rect.height),
  };
}

function mouse(type: string, clientX: number, clientY: number, button = 0): MouseEvent {
  return new MouseEvent(type, { clientX, clientY, button, bubbles: true, cancelable: true });
}

function key(type: "keydown" | "keyup", code: string, keyName: string, init: KeyboardEventInit = {}): KeyboardEvent {
  return new KeyboardEvent(type, { code, key: keyName, bubbles: true, cancelable: true, ...init });
}

describe("<remo-screen-viewer>", () => {
  let restoreSocket: () => void;
  const originalWarn = console.warn;
  const originalError = console.error;

  // モーションのトークンは app.css の :root が定義する（テストでは app.css を読み込まないので、同じ値を当てる）。
  const motionTokens: Record<string, string> = {
    "--motion-fade-in": "320ms",
    "--motion-fade-out": "240ms",
    "--ease-out": "cubic-bezier(0.2, 0.8, 0.2, 1)",
    "--ease-in": "cubic-bezier(0.4, 0, 1, 1)",
  };

  beforeEach(() => {
    restoreSocket = installFakeWebSocket();
    for (const [name, value] of Object.entries(motionTokens)) {
      document.documentElement.style.setProperty(name, value);
    }
    // 意図したログの出力でテストの出力を汚さない。
    console.warn = () => {};
    console.error = () => {};
  });

  afterEach(() => {
    restoreSocket();
    for (const name of Object.keys(motionTokens)) {
      document.documentElement.style.removeProperty(name);
    }
    console.warn = originalWarn;
    console.error = originalError;
  });

  describe("接続", () => {
    it("接続中は Shadow DOM に「接続しています…」を出し、サブプロトコル puppet-remo.v1 で ws-url へ接続する", async () => {
      const viewer = await fixture<RemoScreenViewer>(html`<remo-screen-viewer ws-url=${WS_URL}></remo-screen-viewer>`);
      expect(FakeWebSocket.instances).to.have.length(1);
      expect(FakeWebSocket.last.url).to.equal(WS_URL);
      expect(FakeWebSocket.last.protocols).to.equal("puppet-remo.v1");
      expect(viewer.status).to.equal("connecting");
      expect(overlayText(viewer)).to.equal("接続しています…");
    });

    it("ready の monitor / quality / fps を反映し、canvas をストリームの解像度にする", async () => {
      const { viewer, canvas } = await connect();
      expect(viewer.monitor).to.equal(0);
      expect(viewer.quality).to.equal(60);
      expect(viewer.fps).to.equal(10);
      expect([canvas.width, canvas.height]).to.deep.equal([WIDTH, HEIGHT]);
    });

    it("config で解像度が変わったら canvas を作り直す", async () => {
      const { socket, canvas, viewer } = await connect();
      socket.receiveJson({ type: "config", monitor: 1, width: 128, height: 96, quality: 40, fps: 5 });
      expect([canvas.width, canvas.height]).to.deep.equal([128, 96]);
      expect([viewer.monitor, viewer.quality, viewer.fps]).to.deep.equal([1, 40, 5]);
    });

    it("要素を外すと接続を 1000 で閉じる", async () => {
      const { viewer, socket } = await connect();
      viewer.remove();
      expect(socket.closedByClient).to.equal(1000);
    });
  });

  describe("タイルの描画", () => {
    it("キーフレームのタイル（JPEG）を復号して canvas の (x, y) に描き、表示を open にする", async () => {
      const { viewer, socket, canvas } = await connect();
      socket.receive(
        await frame({
          keyframe: true,
          seq: 1,
          width: WIDTH,
          height: HEIGHT,
          tiles: [
            ["red", 0, 0, 32, 48],
            ["blue", 32, 0, 32, 48],
          ],
        }),
      );
      await untilPixel(canvas, 50, 10, "blue");
      expect(classifyPixel(canvas, 10, 10)).to.equal("red");
      expect(classifyPixel(canvas, 10, 40)).to.equal("red");
      expect(classifyPixel(canvas, 50, 40)).to.equal("blue");
      await until(() => viewer.status === "open", "status is open");
      await viewer.updateComplete;
      // 退出はフェードアウトのあとに取り除く（M5）。
      expect(viewer.shadowRoot?.querySelector(".overlay")?.hasAttribute("data-leaving")).to.equal(true);
      await until(() => overlayText(viewer) === null, "overlay is faded out");
    });

    it("差分フレームは既存の内容の上に重ねて描く", async () => {
      const { socket, canvas } = await connect();
      socket.receive(await fullKeyframe(1, "red"));
      await untilPixel(canvas, 40, 24, "red");
      socket.receive(await diffFrame(2, "green", 16, 16));
      await untilPixel(canvas, 24, 24, "green");
      expect(classifyPixel(canvas, 4, 4)).to.equal("red");
      expect(classifyPixel(canvas, 40, 40)).to.equal("red");
    });

    it("連番が古い（最後に描いた連番以下の）差分フレームは捨てる", async () => {
      const { socket, canvas } = await connect();
      socket.receive(await fullKeyframe(10, "red"));
      socket.receive(await diffFrame(11, "green", 0, 0));
      // 同じ連番と、古い連番。どちらも捨てる。
      socket.receive(await diffFrame(11, "blue", 32, 0));
      socket.receive(await diffFrame(5, "blue", 32, 16));
      // 描画は連番の順に直列なので、後続の目印が描かれたら、先の 2 つの処理は終わっている。
      socket.receive(await diffFrame(12, "green", 48, 32));
      await untilPixel(canvas, 56, 40, "green");
      expect(classifyPixel(canvas, 8, 8)).to.equal("green");
      expect(classifyPixel(canvas, 40, 8)).to.equal("red");
      expect(classifyPixel(canvas, 40, 24)).to.equal("red");
    });

    it("キーフレームは連番が小さくても全面の置き換えとして採用し、連番を引き継ぐ", async () => {
      const { socket, canvas } = await connect();
      socket.receive(await fullKeyframe(10, "red"));
      socket.receive(await diffFrame(11, "green", 0, 0));
      await untilPixel(canvas, 8, 8, "green");

      // モニター・品質・FPS を変えるとサーバーは連番を 1 から数え直す。タイルは左半分だけ。
      socket.receive(await frame({ keyframe: true, seq: 1, width: WIDTH, height: HEIGHT, tiles: [["blue", 0, 0, 32, 48]] }));
      await untilPixel(canvas, 8, 8, "blue");
      // 旧い内容（赤の背景・緑の差分）は全面で消える。
      expect(classifyPixel(canvas, 50, 10)).to.equal("transparent");
      expect(classifyPixel(canvas, 50, 40)).to.equal("transparent");

      // 引き継いだ連番（1）より新しい差分は描く。
      socket.receive(await diffFrame(2, "green", 48, 32));
      await untilPixel(canvas, 56, 40, "green");
    });

    it("キーフレームの前の差分フレームは描かない（キーフレームを待つ）", async () => {
      const { viewer, socket, canvas } = await connect();
      socket.receive(await diffFrame(1, "green", 0, 0));
      socket.receive(await fullKeyframe(2, "blue"));
      await until(() => viewer.status === "open", "status is open");
      expect(classifyPixel(canvas, 8, 8)).to.equal("blue");
    });

    it("選択中でないモニターのフレームは捨てる", async () => {
      const { socket, canvas } = await connect();
      socket.receive(await fullKeyframe(1, "blue"));
      await untilPixel(canvas, 8, 8, "blue");
      socket.receive(await diffFrame(2, "green", 0, 0, 1));
      socket.receive(await diffFrame(3, "red", 48, 32));
      await untilPixel(canvas, 56, 40, "red");
      expect(classifyPixel(canvas, 8, 8)).to.equal("blue");
    });

    it("解像度が変わったキーフレームは canvas を作り直して描き、差分フレームでは作り直さない", async () => {
      const { socket, canvas } = await connect();
      socket.receive(await fullKeyframe(1, "red"));
      await untilPixel(canvas, 8, 8, "red");

      socket.receive(await frame({ keyframe: false, seq: 2, width: 32, height: 24, tiles: [["green", 0, 0, 16, 16]] }));
      expect([canvas.width, canvas.height]).to.deep.equal([WIDTH, HEIGHT]);

      socket.receive(await frame({ keyframe: true, seq: 3, width: 32, height: 24, tiles: [["blue", 0, 0, 32, 24]] }));
      expect([canvas.width, canvas.height]).to.deep.equal([32, 24]);
      await untilPixel(canvas, 8, 8, "blue");
    });

    it("壊れたフレームは捨て、続きのフレームを描く", async () => {
      const { socket, canvas } = await connect();
      socket.receive(new ArrayBuffer(3));
      socket.receive(await fullKeyframe(1, "green"));
      await untilPixel(canvas, 8, 8, "green");
    });

    it("未知・不正な JSON のメッセージは捨てる", async () => {
      const { viewer, socket } = await connect({ lease: heldByMe });
      socket.receiveJson({ type: "nope" });
      socket.receiveJson({ type: "lease", state: "held" });
      socket.receive("not json");
      await viewer.updateComplete;
      expect(viewer.status).to.equal("connecting");
    });
  });

  describe("入力の有効・無効（lease.mine）", () => {
    it("操作権を持つ（held・mine: true）なら、readonly 属性が付いたままでも入力を送る", async () => {
      const { viewer, socket, canvas } = await connect({ readonly: true, lease: heldByMe });
      expect(viewer.hasAttribute("readonly")).to.equal(true);
      const rect = canvas.getBoundingClientRect();
      const clientX = rect.left + 100;
      const clientY = rect.top + 60;
      canvas.dispatchEvent(mouse("mousedown", clientX, clientY));
      expect(socket.sentMessages).to.deep.equal([{ type: "input.mouse", action: "down", ...expectedPoint(canvas, clientX, clientY), button: "left" }]);
    });

    it("readonly 属性が無くても、操作権を持たない（mine: false）なら入力を送らない", async () => {
      const { viewer, socket, canvas, sink } = await connect({ lease: heldByOther });
      expect(viewer.hasAttribute("readonly")).to.equal(false);
      const rect = canvas.getBoundingClientRect();
      const down = mouse("mousedown", rect.left + 10, rect.top + 10);
      canvas.dispatchEvent(down);
      canvas.dispatchEvent(mouse("mousemove", rect.left + 20, rect.top + 20));
      const wheel = new WheelEvent("wheel", { clientX: rect.left + 10, clientY: rect.top + 10, deltaY: 100, bubbles: true, cancelable: true });
      canvas.dispatchEvent(wheel);
      const keydown = key("keydown", "KeyA", "a");
      sink.dispatchEvent(keydown);
      sink.dispatchEvent(new CompositionEvent("compositionend", { data: "あ", bubbles: true }));
      // 移動はアニメーションフレームで送るので、1 フレーム後まで見る。
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
      expect(socket.sent).to.deep.equal([]);
      expect(down.defaultPrevented).to.equal(false);
      expect(wheel.defaultPrevented).to.equal(false);
      expect(keydown.defaultPrevented).to.equal(false);
    });

    it("pending_approval は、承認待ちの本人（mine: true）でも入力を送らない", async () => {
      const { socket, canvas } = await connect({ lease: { state: "pending_approval", mine: true } });
      const rect = canvas.getBoundingClientRect();
      canvas.dispatchEvent(mouse("mousedown", rect.left + 10, rect.top + 10));
      expect(socket.sent).to.deep.equal([]);
    });

    it("WebSocket の lease で操作権を得たら送り始め、失ったら送らなくなる（押下中の状態は up を送らず捨てる）", async () => {
      const { viewer, socket, canvas, sink } = await connect({ lease: idle });
      const rect = canvas.getBoundingClientRect();
      canvas.dispatchEvent(mouse("mousedown", rect.left + 10, rect.top + 10));
      expect(socket.sent).to.deep.equal([]);

      socket.receiveJson({ type: "lease", ...heldByMe });
      await viewer.updateComplete;
      sink.dispatchEvent(key("keydown", "KeyA", "a"));
      expect(socket.sentMessages).to.deep.equal([{ type: "input.key", action: "down", code: "KeyA", key: "a", modifiers: [] }]);

      socket.receiveJson({ type: "lease", ...idle });
      sink.dispatchEvent(key("keyup", "KeyA", "a"));
      sink.dispatchEvent(key("keydown", "KeyB", "b"));
      sink.dispatchEvent(new FocusEvent("blur"));
      expect(socket.sent).to.have.length(1);
    });
  });

  describe("入力の送信", () => {
    it("mousedown は、canvas 上の座標をストリームの座標へ変換して送り、window の mouseup で up を送る", async () => {
      const { socket, canvas } = await connect({ lease: heldByMe });
      const rect = canvas.getBoundingClientRect();
      expect(rect.width).to.equal(DISPLAY_WIDTH);
      const down = mouse("mousedown", rect.left + 100, rect.top + 60, 2);
      canvas.dispatchEvent(down);
      expect(down.defaultPrevented).to.equal(true);
      // 表示 320 × 240 → ストリーム 64 × 48: (100, 60) → (20, 12)。
      expect(socket.sentMessages).to.deep.equal([{ type: "input.mouse", action: "down", x: 20, y: 12, button: "right" }]);

      // canvas の外（さらに右下）で離しても up を送る。座標は範囲へ丸める。
      window.dispatchEvent(mouse("mouseup", rect.left + 10_000, rect.top + 10_000, 2));
      expect(socket.sentMessages[1]).to.deep.equal({ type: "input.mouse", action: "up", x: WIDTH - 1, y: HEIGHT - 1, button: "right" });

      // もう押下中ではないので、次の mouseup は送らない。
      window.dispatchEvent(mouse("mouseup", rect.left + 10, rect.top + 10, 2));
      expect(socket.sent).to.have.length(2);
    });

    it("戻る・進むのボタンは送らない", async () => {
      const { socket, canvas } = await connect({ lease: heldByMe });
      const rect = canvas.getBoundingClientRect();
      canvas.dispatchEvent(mouse("mousedown", rect.left + 10, rect.top + 10, 3));
      expect(socket.sent).to.deep.equal([]);
    });

    it("mousemove は 1 アニメーションフレームに 1 回、最後の位置だけ送る", async () => {
      const { socket, canvas } = await connect({ lease: heldByMe });
      const rect = canvas.getBoundingClientRect();
      canvas.dispatchEvent(mouse("mousemove", rect.left + 5, rect.top + 5));
      canvas.dispatchEvent(mouse("mousemove", rect.left + 150, rect.top + 100));
      await until(() => socket.sent.length > 0, "a move was sent");
      expect(socket.sentMessages).to.deep.equal([{ type: "input.mouse", action: "move", ...expectedPoint(canvas, rect.left + 150, rect.top + 100) }]);
      expect(socket.sentMessages[0]).to.include({ x: 30, y: 20 });
    });

    const wheelCases: { name: string; deltaMode: number; deltaX: number; deltaY: number; dx: number; dy: number }[] = [
      { name: "PIXEL は ÷100", deltaMode: 0, deltaX: 100, deltaY: 200, dx: 1, dy: 2 },
      { name: "LINE は ÷3", deltaMode: 1, deltaX: -3, deltaY: 6, dx: -1, dy: 2 },
      { name: "PAGE は ×10", deltaMode: 2, deltaX: 0, deltaY: -1, dx: 0, dy: -10 },
      { name: "上限 50 へ丸める", deltaMode: 2, deltaX: 100, deltaY: -100, dx: 50, dy: -50 },
    ];
    for (const c of wheelCases) {
      it(`wheel の deltaMode を換算してノッチで送る（${c.name}）`, async () => {
        const { socket, canvas } = await connect({ lease: heldByMe });
        const rect = canvas.getBoundingClientRect();
        const event = new WheelEvent("wheel", {
          clientX: rect.left + 160,
          clientY: rect.top + 120,
          deltaMode: c.deltaMode,
          deltaX: c.deltaX,
          deltaY: c.deltaY,
          bubbles: true,
          cancelable: true,
        });
        canvas.dispatchEvent(event);
        expect(event.defaultPrevented).to.equal(true);
        expect(socket.sentMessages).to.deep.equal([{ type: "input.mouse", action: "wheel", x: 32, y: 24, dx: c.dx, dy: c.dy }]);
      });
    }

    it("キーの down / up を code・key・修飾キー付きで送り、IME 処理中のキーは送らない", async () => {
      const { socket, sink } = await connect({ lease: heldByMe });
      const down = key("keydown", "KeyA", "A", { shiftKey: true, ctrlKey: true });
      sink.dispatchEvent(down);
      expect(down.defaultPrevented).to.equal(true);
      sink.dispatchEvent(key("keyup", "KeyA", "A"));
      // down を送っていないキーの up と、IME 処理中のキーは送らない。
      sink.dispatchEvent(key("keyup", "Enter", "Enter"));
      sink.dispatchEvent(key("keydown", "KeyB", "Process", { isComposing: true }));
      expect(socket.sentMessages).to.deep.equal([
        { type: "input.key", action: "down", code: "KeyA", key: "A", modifiers: ["ctrl", "shift"] },
        { type: "input.key", action: "up", code: "KeyA", key: "A", modifiers: [] },
      ]);
    });

    it("IME で確定した文字列を input.text で送り、入力要素を空にする", async () => {
      const { socket, sink } = await connect({ lease: heldByMe });
      sink.value = "こんにちは";
      sink.dispatchEvent(new CompositionEvent("compositionend", { data: "こんにちは", bubbles: true }));
      expect(socket.sentMessages).to.deep.equal([{ type: "input.text", text: "こんにちは" }]);
      expect(sink.value).to.equal("");
    });

    it("送信が 64 KiB を超えるときは送らず、remo:error（invalid_argument）を出す", async () => {
      const { viewer, socket, sink } = await connect({ lease: heldByMe });
      const errors: ViewerErrorDetail[] = [];
      viewer.addEventListener("remo:error", (event) => errors.push((event as CustomEvent<ViewerErrorDetail>).detail));
      sink.dispatchEvent(new CompositionEvent("compositionend", { data: "a".repeat(70_000), bubbles: true }));
      expect(socket.sent).to.deep.equal([]);
      expect(errors).to.have.length(1);
      expect(errors[0]?.code).to.equal("invalid_argument");
    });

    it("フォーカスを失ったら、押下中のすべてのキー・ボタンに up を送る", async () => {
      const { socket, canvas, sink } = await connect({ lease: heldByMe });
      const rect = canvas.getBoundingClientRect();
      sink.dispatchEvent(key("keydown", "KeyA", "a"));
      sink.dispatchEvent(key("keydown", "ShiftLeft", "Shift", { shiftKey: true }));
      canvas.dispatchEvent(mouse("mousedown", rect.left + 100, rect.top + 60, 0));
      expect(socket.sent).to.have.length(3);

      sink.dispatchEvent(new FocusEvent("blur"));
      expect(socket.sentMessages.slice(3)).to.deep.equal([
        { type: "input.key", action: "up", code: "KeyA", key: "a", modifiers: [] },
        { type: "input.key", action: "up", code: "ShiftLeft", key: "Shift", modifiers: [] },
        { type: "input.mouse", action: "up", x: 20, y: 12, button: "left" },
      ]);

      // 離し済みの扱いなので、その後の keyup・mouseup は重ねて送らない。
      sink.dispatchEvent(key("keyup", "KeyA", "a"));
      window.dispatchEvent(mouse("mouseup", rect.left + 100, rect.top + 60, 0));
      expect(socket.sent).to.have.length(6);

      // 押下中のものが無ければ、フォーカスを失っても何も送らない。
      sink.dispatchEvent(new FocusEvent("blur"));
      expect(socket.sent).to.have.length(6);
    });
  });

  // C3: 見えない入力要素にフォーカスがある間（= キー入力がホストへ送られる間）は、ホストの外側に 2px solid・offset 2 のリングを出す。
  describe("フォーカスリング", () => {
    it("入力要素にフォーカスがある間だけ、ホストに 2px solid・offset 2 の outline が付く", async () => {
      const { viewer, sink } = await connect({ lease: heldByMe });
      viewer.style.setProperty("--focus", "rgb(1, 2, 3)"); // app.css の :root が定義するトークン（テストでは app.css を読まない）
      expect(getComputedStyle(viewer).outlineStyle).to.equal("none");

      sink.focus();
      const style = getComputedStyle(viewer);
      expect(style.outlineStyle).to.equal("solid");
      expect(style.outlineWidth).to.equal("2px");
      expect(style.outlineColor).to.equal("rgb(1, 2, 3)");
      expect(style.outlineOffset).to.equal("2px");

      sink.blur();
      expect(getComputedStyle(viewer).outlineStyle).to.equal("none");
    });
  });

  describe("イベント", () => {
    it("ready と lease のたびに remo:lease-changed を（bubbles・composed で）出し、detail は lease の内容", async () => {
      const viewer = await fixture<RemoScreenViewer>(html`<remo-screen-viewer ws-url=${WS_URL}></remo-screen-viewer>`);
      const seen: Lease[] = [];
      let composed = false;
      document.addEventListener("remo:lease-changed", (event) => {
        seen.push((event as CustomEvent<Lease>).detail);
        composed = event.composed && event.bubbles;
      });
      const socket = FakeWebSocket.last;
      socket.open();
      socket.receiveJson({ type: "ready", monitor: 0, width: WIDTH, height: HEIGHT, quality: 60, fps: 10, lease: heldByOther });
      socket.receiveJson({ type: "lease", ...heldByMe });
      socket.receiveJson({ type: "lease", state: "idle", mine: false });
      await viewer.updateComplete;
      expect(seen).to.deep.equal([heldByOther, heldByMe, { state: "idle", holder: undefined, mine: false }]);
      expect(composed).to.equal(true);
    });

    it("error メッセージを remo:error（code・message）にする", async () => {
      const { viewer, socket } = await connect();
      const errors: ViewerErrorDetail[] = [];
      viewer.addEventListener("remo:error", (event) => errors.push((event as CustomEvent<ViewerErrorDetail>).detail));
      socket.receiveJson({ type: "error", code: "lease_required", message: "操作権がありません" });
      expect(errors).to.deep.equal([{ code: "lease_required", message: "操作権がありません" }]);
    });
  });

  describe("切断・再接続・失敗の表示", () => {
    let timers: FakeTimers;

    beforeEach(() => {
      timers = new FakeTimers();
      timers.install();
    });

    afterEach(() => {
      timers.restore();
    });

    it("切断したら「再接続しています」を出し、再接続の後の ready でも remo:lease-changed を出して、入力の可否を作り直す", async () => {
      const { viewer, socket } = await connect({ lease: heldByMe });
      const leases: Lease[] = [];
      viewer.addEventListener("remo:lease-changed", (event) => leases.push((event as CustomEvent<Lease>).detail));
      socket.receive(await fullKeyframe(1, "red"));
      await until(() => viewer.status === "open", "status is open");

      socket.serverClose(1006);
      await viewer.updateComplete;
      expect(viewer.status).to.equal("reconnecting");
      expect(overlayText(viewer)).to.equal("切断されました。再接続しています…");
      expect(timers.pendingDelays).to.deep.equal([500]);

      timers.fireNext();
      expect(FakeWebSocket.instances).to.have.length(2);
      const second = FakeWebSocket.last;
      second.open();
      // サーバーは再接続でリースを解放している（idle）。
      second.receiveJson({ type: "ready", monitor: 0, width: WIDTH, height: HEIGHT, quality: 60, fps: 10, lease: idle });
      expect(leases).to.deep.equal([idle]);
      const canvas = viewer.shadowRoot?.querySelector("canvas");
      const sink = viewer.shadowRoot?.querySelector("textarea");
      if (!canvas || !sink) {
        throw new Error("missing canvas or textarea");
      }
      sink.dispatchEvent(key("keydown", "KeyA", "a"));
      expect(second.sent).to.deep.equal([]);

      // キーフレームが描かれるまでは再接続中の表示のまま。描けたら open に戻る。
      expect(viewer.status).to.equal("reconnecting");
      second.receive(await fullKeyframe(1, "green"));
      await until(() => viewer.status === "open", "status is open again");
      await viewer.updateComplete;
      await until(() => overlayText(viewer) === null, "overlay is faded out");

      // 操作権を取り直せば、新しい接続で入力を送れる。
      second.receiveJson({ type: "lease", ...heldByMe });
      sink.dispatchEvent(key("keydown", "KeyB", "b"));
      expect(second.sentMessages).to.deep.equal([{ type: "input.key", action: "down", code: "KeyB", key: "b", modifiers: [] }]);
      expect(leases).to.deep.equal([idle, heldByMe]);
    });

    it("切断すると押下中の入力は up を送らずに捨てる（サーバーが解放する）", async () => {
      const { socket, sink } = await connect({ lease: heldByMe });
      sink.dispatchEvent(key("keydown", "KeyA", "a"));
      socket.serverClose(1006);
      timers.fireNext();
      FakeWebSocket.last.open();
      sink.dispatchEvent(new FocusEvent("blur"));
      expect(FakeWebSocket.last.sent).to.deep.equal([]);
    });

    const failureCases: [number, string][] = [
      [4400, "不正な通信を検出したため、接続を終了しました。ページを再読み込みしてください。"],
      [4401, "認可に失敗しました。ページを再読み込みしてください。"],
      [4429, "同時に閲覧できる上限に達しました。ページを再読み込みしてください。"],
      [4510, "このデバイスは削除または無効化されました。ページを再読み込みしてください。"],
    ];
    for (const [code, text] of failureCases) {
      it(`閉じコード ${code} は再接続せず、failed と再読み込みを促す文言を Shadow DOM に出す`, async () => {
        const { viewer, socket } = await connect({ lease: heldByMe });
        socket.serverClose(code);
        await viewer.updateComplete;
        expect(viewer.status).to.equal("failed");
        expect(overlayText(viewer)).to.equal(text);
        expect(timers.pendingDelays).to.deep.equal([]);
        expect(FakeWebSocket.instances).to.have.length(1);
      });
    }

    it("閉じコード 4503 は killswitch と再読み込みを促す文言を出し、再接続しない", async () => {
      const { viewer, socket } = await connect({ lease: heldByMe });
      socket.serverClose(4503);
      await viewer.updateComplete;
      expect(viewer.status).to.equal("killswitch");
      expect(overlayText(viewer)).to.equal("キルスイッチが有効なため、接続を停止しました。解除後にページを再読み込みしてください。");
      expect(timers.pendingDelays).to.deep.equal([]);
      expect(FakeWebSocket.instances).to.have.length(1);
    });

    it("killswitch メッセージ（active: true）で killswitch の表示にし、active: false では変えない", async () => {
      const { viewer, socket } = await connect({ lease: heldByMe });
      socket.receiveJson({ type: "killswitch", active: false });
      await viewer.updateComplete;
      expect(viewer.status).to.equal("connecting");
      socket.receiveJson({ type: "killswitch", active: true });
      await viewer.updateComplete;
      expect(viewer.status).to.equal("killswitch");
      expect(overlayText(viewer)).to.contain("ページを再読み込みしてください。");
      // サーバーが続けて 4503 で閉じても killswitch のまま。
      socket.serverClose(4503);
      await viewer.updateComplete;
      expect(viewer.status).to.equal("killswitch");
    });

    it("再接続の試行が上限に達したら failed と再読み込みを促す文言を出す", async () => {
      const { viewer, socket } = await connect({ lease: heldByMe });
      socket.serverClose(1006);
      for (let attempt = 1; attempt <= MAX_RECONNECTS; attempt++) {
        timers.fireNext();
        FakeWebSocket.last.serverClose(1006);
      }
      await viewer.updateComplete;
      expect(timers.pendingDelays).to.deep.equal([]);
      expect(viewer.status).to.equal("failed");
      expect(overlayText(viewer)).to.equal("接続できませんでした。ページを再読み込みしてください。");
    });

    it("WebSocket を作れなければ failed にする", async () => {
      FakeWebSocket.throwOnConstruct = true;
      const viewer = await fixture<RemoScreenViewer>(html`<remo-screen-viewer ws-url=${WS_URL}></remo-screen-viewer>`);
      expect(viewer.status).to.equal("failed");
      expect(overlayText(viewer)).to.equal("接続できませんでした。ページを再読み込みしてください。");
    });

    // M1: スピナーの回転は :root の --spinner-duration（ガイドの 0.9s）を参照する（Shadow DOM にも継承される）。
    it("スピナーの回転は --spinner-duration を参照する", async () => {
      const { viewer } = await connect({ lease: heldByMe });
      viewer.style.setProperty("--spinner-duration", "0.9s");
      const spinner = viewer.shadowRoot?.querySelector(".overlay .spinner");
      expect(spinner).to.not.equal(null);
      expect(getComputedStyle(spinner as Element).animationDuration).to.equal("0.9s");
    });

    // C4: 状態はアイコン + 文言（+ 状態色）で伝える。接続中・再接続中はスピナー、失敗は danger、キルスイッチは warning。
    it("接続中・再接続中はスピナー、失敗は danger のアイコン、キルスイッチは warning のアイコンを添える", async () => {
      const { viewer, socket } = await connect({ lease: heldByMe });
      const overlay = () => viewer.shadowRoot?.querySelector(".overlay");
      expect(overlay()?.querySelector(".spinner")).to.not.equal(null);
      expect(overlay()?.querySelector("svg")).to.equal(null);

      socket.serverClose(4401);
      await viewer.updateComplete;
      expect(overlay()?.querySelector("svg.tone-danger")).to.not.equal(null);
      expect(overlay()?.querySelector(".spinner")).to.equal(null);
    });

    it("キルスイッチは warning のアイコンを添える", async () => {
      const { viewer, socket } = await connect({ lease: heldByMe });
      socket.serverClose(4503);
      await viewer.updateComplete;
      expect(viewer.shadowRoot?.querySelector(".overlay svg.tone-warning")).to.not.equal(null);
    });

    // M5: 出現はフェードイン（トークンは :root のカスタムプロパティ。Shadow DOM の css が参照する）。
    it("オーバーレイの出現はフェードインのアニメーションを持つ", async () => {
      const viewer = await fixture<RemoScreenViewer>(html`<remo-screen-viewer ws-url=${WS_URL}></remo-screen-viewer>`);
      const overlay = viewer.shadowRoot?.querySelector(".overlay");
      const names = overlay?.getAnimations().map((a) => (a as CSSAnimation).animationName) ?? [];
      expect(names).to.include("overlay-fade-in");
    });
  });
});
