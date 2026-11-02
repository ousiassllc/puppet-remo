// lib/ws.ts のテスト（docs/architecture/frontend.md §6.4・§9、docs/api/endpoints.md §5）。
// 偽の WebSocket と偽のタイマーで再接続の方針を確かめる（実時間では待たない）。
import { expect } from "@open-wc/testing";
import { FakeTimers } from "../testing/fake-timers";
import { FakeWebSocket, installFakeWebSocket } from "../testing/fake-websocket";
import { MAX_MESSAGE_BYTES, MAX_RECONNECTS, MessageTooLargeError, NO_RECONNECT_CLOSE_CODES, SUBPROTOCOL, WsClient, reconnectDelay, type WsMessage } from "./ws";

describe("reconnectDelay", () => {
  it("500 ms から倍々で、15 s を上限にする", () => {
    const delays = Array.from({ length: 10 }, (_, i) => reconnectDelay(i + 1));
    expect(delays).to.deep.equal([500, 1000, 2000, 4000, 8000, 15000, 15000, 15000, 15000, 15000]);
  });
});

describe("WsClient", () => {
  let restoreSocket: () => void;
  let timers: FakeTimers;
  let messages: WsMessage[];
  let closes: number[];
  let opens: number;
  let giveUps: number;
  let client: WsClient;

  const consoleMethods = ["warn", "error"] as const;
  const originalConsole = { warn: console.warn, error: console.error };

  beforeEach(() => {
    restoreSocket = installFakeWebSocket();
    timers = new FakeTimers();
    timers.install();
    messages = [];
    closes = [];
    opens = 0;
    giveUps = 0;
    // 意図したログの出力でテストの出力を汚さない。
    for (const method of consoleMethods) {
      console[method] = () => {};
    }
    client = new WsClient("wss://remo.example.test/ws/screen", {
      onOpen: () => opens++,
      onMessage: (message) => messages.push(message),
      onClose: (code) => closes.push(code),
      onGiveUp: () => giveUps++,
    });
  });

  afterEach(() => {
    client.close();
    timers.restore();
    restoreSocket();
    for (const method of consoleMethods) {
      console[method] = originalConsole[method];
    }
  });

  describe("接続", () => {
    it("サブプロトコル puppet-remo.v1 で接続し、バイナリは ArrayBuffer で受ける", () => {
      client.connect();
      const socket = FakeWebSocket.last;
      expect(socket.url).to.equal("wss://remo.example.test/ws/screen");
      expect(socket.protocols).to.equal(SUBPROTOCOL);
      expect(SUBPROTOCOL).to.equal("puppet-remo.v1");
      expect(socket.binaryType).to.equal("arraybuffer");
    });

    it("開いたら onOpen を呼ぶ", () => {
      client.connect();
      FakeWebSocket.last.open();
      expect(opens).to.equal(1);
    });

    it("WebSocket を作れなければ再試行せず onGiveUp を呼ぶ", () => {
      FakeWebSocket.throwOnConstruct = true;
      client.connect();
      expect(giveUps).to.equal(1);
      expect(timers.pendingDelays).to.deep.equal([]);
    });
  });

  describe("受信", () => {
    it("テキストは JSON にして kind: text で渡す", () => {
      client.connect();
      FakeWebSocket.last.open();
      FakeWebSocket.last.receive('{"type":"pong"}');
      expect(messages).to.deep.equal([{ kind: "text", message: { type: "pong" } }]);
    });

    it("バイナリは kind: binary で渡す", () => {
      client.connect();
      const data = new Uint8Array([1, 2, 3]).buffer;
      FakeWebSocket.last.receive(data);
      expect(messages).to.have.length(1);
      const [message] = messages;
      expect(message?.kind).to.equal("binary");
      expect(message?.kind === "binary" ? new Uint8Array(message.data) : null).to.deep.equal(new Uint8Array([1, 2, 3]));
    });

    it("JSON でないテキストは捨てる", () => {
      client.connect();
      FakeWebSocket.last.receive("not json");
      expect(messages).to.deep.equal([]);
    });

    it("close() の後は、古い接続のイベントでコールバックを呼ばない", () => {
      client.connect();
      const socket = FakeWebSocket.last;
      client.close();
      socket.open();
      socket.receiveJson({ type: "pong" });
      socket.serverClose(1006);
      expect({ opens, messages: messages.length, closes }).to.deep.equal({ opens: 0, messages: 0, closes: [] });
      expect(timers.pendingDelays).to.deep.equal([]);
    });
  });

  describe("送信", () => {
    it("開いている接続へ JSON で送る", () => {
      client.connect();
      const socket = FakeWebSocket.last;
      socket.open();
      expect(client.send({ type: "ping" })).to.equal(true);
      expect(socket.sent).to.deep.equal(['{"type":"ping"}']);
    });

    it("開いていなければ送らず false を返す", () => {
      expect(client.send({ type: "ping" })).to.equal(false);
      client.connect();
      expect(client.send({ type: "ping" })).to.equal(false);
      expect(FakeWebSocket.last.sent).to.deep.equal([]);
    });

    it("64 KiB ちょうどは送れ、超えると送らずに MessageTooLargeError を投げる", () => {
      client.connect();
      const socket = FakeWebSocket.last;
      socket.open();
      // {"t":""} の 8 バイトを除いた分の ASCII で、ちょうど上限になる。
      const exact = { t: "a".repeat(MAX_MESSAGE_BYTES - 8) };
      expect(client.send(exact)).to.equal(true);
      expect(new TextEncoder().encode(socket.sent[0]).length).to.equal(MAX_MESSAGE_BYTES);

      const over = { t: "a".repeat(MAX_MESSAGE_BYTES - 7) };
      expect(() => client.send(over)).to.throw(MessageTooLargeError);
      expect(socket.sent).to.have.length(1);
    });

    it("上限は文字数ではなく UTF-8 のバイト数で数える", () => {
      client.connect();
      FakeWebSocket.last.open();
      // 21846 文字 × 3 バイト = 65538 バイト（文字数は 65536 未満）。
      const message = { t: "あ".repeat(21846) };
      let error: unknown;
      try {
        client.send(message);
      } catch (e) {
        error = e;
      }
      expect(error).to.be.instanceOf(MessageTooLargeError);
      expect((error as MessageTooLargeError).bytes).to.be.greaterThan(MAX_MESSAGE_BYTES);
      expect(FakeWebSocket.last.sent).to.deep.equal([]);
    });

    it("接続が開いていなくても、大きすぎるメッセージは例外にする", () => {
      expect(() => client.send({ t: "a".repeat(MAX_MESSAGE_BYTES) })).to.throw(MessageTooLargeError);
    });
  });

  describe("再接続", () => {
    it("閉じたら 500 ms から倍々で待ち、15 s を上限にして、10 回で諦める", () => {
      client.connect();
      const waited: number[] = [];
      for (let attempt = 1; attempt <= MAX_RECONNECTS; attempt++) {
        FakeWebSocket.last.serverClose(1006);
        expect(giveUps).to.equal(0);
        waited.push(...timers.pendingDelays);
        timers.fireNext();
        expect(FakeWebSocket.instances).to.have.length(attempt + 1);
      }
      expect(waited).to.deep.equal([500, 1000, 2000, 4000, 8000, 15000, 15000, 15000, 15000, 15000]);

      // 11 回目の失敗では待たずに諦める。
      FakeWebSocket.last.serverClose(1006);
      expect(timers.pendingDelays).to.deep.equal([]);
      expect(giveUps).to.equal(1);
      expect(closes).to.have.length(MAX_RECONNECTS + 1);
    });

    it("接続が開いただけでは試行の数を戻さない（直ぐ閉じる繰り返しを数える）", () => {
      client.connect();
      const waited: number[] = [];
      for (let i = 0; i < 3; i++) {
        FakeWebSocket.last.open();
        FakeWebSocket.last.serverClose(1006);
        waited.push(...timers.pendingDelays);
        timers.fireNext();
      }
      expect(waited).to.deep.equal([500, 1000, 2000]);
    });

    it("サーバーから 1 つでもメッセージを受けたら、試行の数を戻す", () => {
      client.connect();
      for (let i = 0; i < 3; i++) {
        FakeWebSocket.last.serverClose(1006);
        timers.fireNext();
      }
      FakeWebSocket.last.open();
      FakeWebSocket.last.receiveJson({ type: "pong" });
      FakeWebSocket.last.serverClose(1006);
      expect(timers.pendingDelays).to.deep.equal([500]);
    });

    it("再接続でも onOpen を呼び、同じ URL とサブプロトコルで接続する", () => {
      client.connect();
      FakeWebSocket.last.open();
      FakeWebSocket.last.serverClose(1006);
      timers.fireNext();
      const second = FakeWebSocket.last;
      expect(FakeWebSocket.instances).to.have.length(2);
      expect(second.url).to.equal("wss://remo.example.test/ws/screen");
      expect(second.protocols).to.equal(SUBPROTOCOL);
      second.open();
      expect(opens).to.equal(2);
    });

    it("閉じるたびに onClose を呼ぶ（再接続する場合も）", () => {
      client.connect();
      FakeWebSocket.last.serverClose(1006);
      timers.fireNext();
      FakeWebSocket.last.serverClose(1001);
      expect(closes).to.deep.equal([1006, 1001]);
    });

    for (const code of [4400, 4401, 4429, 4503, 4510]) {
      it(`閉じコード ${code} では再接続しない`, () => {
        client.connect();
        FakeWebSocket.last.open();
        FakeWebSocket.last.serverClose(code);
        expect(closes).to.deep.equal([code]);
        expect(timers.pendingDelays).to.deep.equal([]);
        expect(FakeWebSocket.instances).to.have.length(1);
        // 諦め（onGiveUp）は再試行の上限のときだけ。
        expect(giveUps).to.equal(0);
      });
    }

    it("再接続しない閉じコードは 4400 / 4401 / 4429 / 4503 / 4510 だけ", () => {
      expect([...NO_RECONNECT_CLOSE_CODES].sort()).to.deep.equal([4400, 4401, 4429, 4503, 4510]);
    });

    it("正常終了（1000）や異常終了（1006）は再接続する", () => {
      for (const code of [1000, 1001, 1006, 1011]) {
        const fresh = new WsClient("wss://remo.example.test/ws/screen", {});
        fresh.connect();
        FakeWebSocket.last.serverClose(code);
        expect(timers.pendingDelays, `code ${code}`).to.deep.equal([500]);
        timers.fireNext();
        fresh.close();
      }
    });

    it("close() は待機中の再接続を取り消し、接続を 1000 で閉じる", () => {
      client.connect();
      const first = FakeWebSocket.last;
      first.serverClose(1006);
      expect(timers.pendingDelays).to.deep.equal([500]);
      client.close();
      expect(timers.pendingDelays).to.deep.equal([]);
      expect(FakeWebSocket.instances).to.have.length(1);

      client.connect();
      const second = FakeWebSocket.last;
      client.close();
      expect(second.closedByClient).to.equal(1000);
    });
  });
});
