// protocol.ts のテスト（メッセージの検証とバイナリフレームの復号。docs/api/endpoints.md §5.3・§5.4）。
import { expect } from "@open-wc/testing";
import { buildFrame } from "../testing/frames";
import { FrameError, GROUP_MONITOR, parseFrame, parseServerMessage } from "./protocol";

describe("parseFrame", () => {
  const jpegA = new Uint8Array([0xff, 0xd8, 1, 2, 3, 0xff, 0xd9]);
  const jpegB = new Uint8Array([0xff, 0xd8, 9, 0xff, 0xd9]);

  it("ヘッダーとタイルをリトルエンディアンで復号する", () => {
    const buffer = buildFrame({
      keyframe: true,
      monitor: 2,
      seq: 70_000,
      width: 1920,
      height: 1080,
      tiles: [
        { x: 0, y: 0, w: 64, h: 64, jpeg: jpegA },
        { x: 640, y: 128, w: 128, h: 64, jpeg: jpegB },
      ],
    });
    const frame = parseFrame(buffer);
    expect(frame).to.include({ keyframe: true, monitor: 2, seq: 70_000, width: 1920, height: 1080 });
    expect(frame.tiles).to.have.length(2);
    expect(frame.tiles.map(({ x, y, w, h }) => ({ x, y, w, h }))).to.deep.equal([
      { x: 0, y: 0, w: 64, h: 64 },
      { x: 640, y: 128, w: 128, h: 64 },
    ]);
    expect(frame.tiles[0]?.jpeg).to.deep.equal(jpegA);
    expect(frame.tiles[1]?.jpeg).to.deep.equal(jpegB);
  });

  it("フラグの bit0 がなければ差分フレーム", () => {
    const frame = parseFrame(buildFrame({ keyframe: false, seq: 1, width: 64, height: 64, tiles: [] }));
    expect(frame.keyframe).to.equal(false);
    expect(frame.tiles).to.deep.equal([]);
  });

  it("JPEG は元のバッファの view（コピーしない）", () => {
    const buffer = buildFrame({ keyframe: true, seq: 1, width: 64, height: 64, tiles: [{ x: 0, y: 0, w: 64, h: 64, jpeg: jpegA }] });
    const frame = parseFrame(buffer);
    expect(frame.tiles[0]?.jpeg.buffer).to.equal(buffer);
  });

  it("ヘッダーより短ければ FrameError", () => {
    expect(() => parseFrame(new ArrayBuffer(13))).to.throw(FrameError);
  });

  it("バージョンが 1 でなければ FrameError", () => {
    const buffer = buildFrame({ keyframe: true, seq: 1, width: 64, height: 64, tiles: [] });
    new DataView(buffer).setUint8(0, 2);
    expect(() => parseFrame(buffer)).to.throw(FrameError, "version");
  });

  it("タイルのヘッダーが範囲外なら FrameError", () => {
    const buffer = buildFrame({ keyframe: true, seq: 1, width: 64, height: 64, tiles: [] });
    new DataView(buffer).setUint16(12, 1, true);
    expect(() => parseFrame(buffer)).to.throw(FrameError, "header");
  });

  it("タイルのデータが範囲外なら FrameError", () => {
    const buffer = buildFrame({ keyframe: true, seq: 1, width: 64, height: 64, tiles: [{ x: 0, y: 0, w: 64, h: 64, jpeg: jpegA }] });
    new DataView(buffer).setUint32(14 + 8, jpegA.length + 1, true);
    expect(() => parseFrame(buffer)).to.throw(FrameError, "data");
  });
});

describe("parseServerMessage", () => {
  const lease = { state: "held", holder: { kind: "web", label: "iPad" }, mine: true };

  it("ready（source が無ければ monitor。古いサーバーとの互換）", () => {
    const message = { type: "ready", monitor: 0, width: 1920, height: 1080, quality: 60, fps: 10, lease };
    expect(parseServerMessage(message)).to.deep.equal({ ...message, source: "monitor" });
  });

  it("ready・config のグループ源は source と原点を持つ（原点が不正・無ければ origin なし）", () => {
    const config = { type: "config", monitor: 0, source: "group", width: 400, height: 300, origin: { x: -20, y: 80 }, quality: 60, fps: 10 };
    expect(parseServerMessage(config)).to.deep.equal(config);
    expect(parseServerMessage({ ...config, origin: undefined })).to.not.have.property("origin");
    expect(parseServerMessage({ ...config, origin: { x: 1.5, y: 0 } })).to.not.have.property("origin");
    const ready = { type: "ready", monitor: 0, source: "group", width: 0, height: 0, quality: 60, fps: 10, lease };
    expect(parseServerMessage(ready)).to.deep.equal(ready);
  });

  it("target（active・halted。blocked_by・notices・excluded。タイトルは持たない）", () => {
    expect(
      parseServerMessage({
        type: "target", state: "halted", process: "notepad.exe", reason: "related_cross_process", members: 2,
        blocked_by: { window_id: "9", process: "explorer.exe", pid: 5230, reason: "related_cross_process" },
      }),
    ).to.deep.equal({
      type: "target",
      target: {
        state: "halted", process: "notepad.exe", reason: "related_cross_process", reselect: false, members: 2, notices: [], excluded: [],
        blockedBy: { window_id: "9", process: "explorer.exe", pid: 5230, reason: "related_cross_process" },
      },
    });
    const partial = parseServerMessage({ type: "target", state: "active", members: 1, notices: ["capture_partial"], excluded: [{ window_id: "2", reason: "related_unverifiable" }] });
    expect(partial).to.deep.include({ type: "target" });
    expect(partial && partial.type === "target" ? partial.target.notices : []).to.deep.equal(["capture_partial"]);
    expect(parseServerMessage({ type: "target", state: "paused" })).to.equal(null);
  });

  it("グループ源のフレームは原点（i32）を読み、モニター源のフレームは原点なし", () => {
    const group = parseFrame(buildFrame({ keyframe: true, origin: { x: -30, y: 80 }, seq: 1, width: 64, height: 64, tiles: [] }));
    expect(group).to.include({ group: true, monitor: GROUP_MONITOR });
    expect(group.origin).to.deep.equal({ x: -30, y: 80 });
    const monitor = parseFrame(buildFrame({ keyframe: true, monitor: 1, seq: 1, width: 64, height: 64, tiles: [] }));
    expect(monitor.group).to.equal(false);
    expect(monitor.origin).to.equal(null);
  });

  it("ready の値が不正なら null（負数・小数・lease 欠落）", () => {
    const base = { type: "ready", monitor: 0, width: 1920, height: 1080, quality: 60, fps: 10, lease };
    expect(parseServerMessage({ ...base, width: -1 })).to.equal(null);
    expect(parseServerMessage({ ...base, fps: 1.5 })).to.equal(null);
    expect(parseServerMessage({ ...base, lease: undefined })).to.equal(null);
    expect(parseServerMessage({ ...base, lease: { state: "held" } })).to.equal(null);
  });

  it("config", () => {
    const message = { type: "config", monitor: 1, width: 1280, height: 720, quality: 40, fps: 5 };
    expect(parseServerMessage(message)).to.deep.equal({ ...message, source: "monitor" });
    expect(parseServerMessage({ ...message, monitor: "1" })).to.equal(null);
  });

  it("lease は lease の内容そのもの（type を含む）", () => {
    expect(parseServerMessage({ type: "lease", ...lease })).to.deep.equal({ type: "lease", lease });
    expect(parseServerMessage({ type: "lease", state: "idle", mine: false })).to.deep.equal({
      type: "lease",
      lease: { state: "idle", holder: undefined, mine: false },
    });
    expect(parseServerMessage({ type: "lease", state: "idle" })).to.equal(null);
  });

  it("killswitch・error・pong", () => {
    expect(parseServerMessage({ type: "killswitch", active: true })).to.deep.equal({ type: "killswitch", active: true });
    expect(parseServerMessage({ type: "killswitch", active: "yes" })).to.equal(null);
    expect(parseServerMessage({ type: "error", code: "forbidden", message: "x" })).to.deep.equal({ type: "error", code: "forbidden", message: "x" });
    expect(parseServerMessage({ type: "error", code: "input_blocked", message: "x", reason: "target_halted" })).to.deep.equal({
      type: "error", code: "input_blocked", message: "x", reason: "target_halted",
    });
    expect(parseServerMessage({ type: "error", code: "forbidden" })).to.equal(null);
    expect(parseServerMessage({ type: "pong" })).to.deep.equal({ type: "pong" });
  });

  it("未知の type・オブジェクトでない値は null", () => {
    expect(parseServerMessage({ type: "nope" })).to.equal(null);
    expect(parseServerMessage(null)).to.equal(null);
    expect(parseServerMessage("ready")).to.equal(null);
    expect(parseServerMessage(42)).to.equal(null);
  });
});
