// /ws/screen のメッセージとバイナリフレームの型・復号（docs/api/endpoints.md §5.3・§5.4）。

export interface LeaseHolder {
  kind: string;
  label: string;
}

// WebSocket の lease メッセージの内容（`type` を除く）。remo:lease-changed の detail になる。
export interface Lease {
  state: "idle" | "pending_approval" | "held";
  holder?: LeaseHolder;
  mine: boolean;
}

export type ServerMessage =
  | { type: "ready"; monitor: number; width: number; height: number; quality: number; fps: number; lease: Lease }
  | { type: "config"; monitor: number; width: number; height: number; quality: number; fps: number }
  | { type: "lease"; lease: Lease }
  | { type: "killswitch"; active: boolean }
  | { type: "error"; code: string; message: string }
  | { type: "pong" };

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isLease(value: unknown): value is Lease {
  return isRecord(value) && typeof value.state === "string" && typeof value.mine === "boolean";
}

function isCount(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0;
}

// parseServerMessage は、JSON をパースした値をサーバーのメッセージとして検証する。形が合わなければ null。
export function parseServerMessage(raw: unknown): ServerMessage | null {
  if (!isRecord(raw)) {
    return null;
  }
  const m = raw;
  switch (m.type) {
    case "ready":
      if (isCount(m.monitor) && isCount(m.width) && isCount(m.height) && isCount(m.quality) && isCount(m.fps) && isLease(m.lease)) {
        return { type: "ready", monitor: m.monitor, width: m.width, height: m.height, quality: m.quality, fps: m.fps, lease: m.lease };
      }
      return null;
    case "config":
      if (isCount(m.monitor) && isCount(m.width) && isCount(m.height) && isCount(m.quality) && isCount(m.fps)) {
        return { type: "config", monitor: m.monitor, width: m.width, height: m.height, quality: m.quality, fps: m.fps };
      }
      return null;
    case "lease":
      // lease メッセージは lease の内容そのもの（`type` を含む）。
      return isLease(m) ? { type: "lease", lease: { state: m.state, holder: m.holder as LeaseHolder | undefined, mine: m.mine } } : null;
    case "killswitch":
      return typeof m.active === "boolean" ? { type: "killswitch", active: m.active } : null;
    case "error":
      return typeof m.code === "string" && typeof m.message === "string" ? { type: "error", code: m.code, message: m.message } : null;
    case "pong":
      return { type: "pong" };
    default:
      return null;
  }
}

export interface Tile {
  x: number;
  y: number;
  w: number;
  h: number;
  jpeg: Uint8Array<ArrayBuffer>;
}

export interface ScreenFrame {
  keyframe: boolean;
  monitor: number;
  seq: number;
  width: number;
  height: number;
  tiles: Tile[];
}

const FRAME_VERSION = 1;
const FRAME_HEADER_BYTES = 14;
const TILE_HEADER_BYTES = 12;
const FLAG_KEYFRAME = 1;

export class FrameError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "FrameError";
  }
}

// parseFrame は、バイナリフレーム（リトルエンディアン。API 仕様 §5.4）を復号する。JPEG は元のバッファの view（コピーしない）。
export function parseFrame(buffer: ArrayBuffer): ScreenFrame {
  if (buffer.byteLength < FRAME_HEADER_BYTES) {
    throw new FrameError("frame is shorter than its header");
  }
  const view = new DataView(buffer);
  if (view.getUint8(0) !== FRAME_VERSION) {
    throw new FrameError(`unsupported frame version ${view.getUint8(0)}`);
  }
  const count = view.getUint16(12, true);
  const tiles: Tile[] = [];
  let offset = FRAME_HEADER_BYTES;
  for (let i = 0; i < count; i++) {
    if (offset + TILE_HEADER_BYTES > buffer.byteLength) {
      throw new FrameError("tile header is out of bounds");
    }
    const jpegLength = view.getUint32(offset + 8, true);
    const start = offset + TILE_HEADER_BYTES;
    if (start + jpegLength > buffer.byteLength) {
      throw new FrameError("tile data is out of bounds");
    }
    tiles.push({
      x: view.getUint16(offset, true),
      y: view.getUint16(offset + 2, true),
      w: view.getUint16(offset + 4, true),
      h: view.getUint16(offset + 6, true),
      jpeg: new Uint8Array(buffer, start, jpegLength),
    });
    offset = start + jpegLength;
  }
  return {
    keyframe: (view.getUint8(1) & FLAG_KEYFRAME) !== 0,
    monitor: view.getUint16(2, true),
    seq: view.getUint32(4, true),
    width: view.getUint16(8, true),
    height: view.getUint16(10, true),
    tiles,
  };
}
