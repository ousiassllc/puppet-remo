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

// 配信の源（#157）。group はスコープ付きリースの対象ウィンドウ T と関連ウィンドウの合成画像（width・height は合成画像の大きさ、
// 物理座標 = origin + 画像の座標）。
export type StreamSource = "monitor" | "group";

export interface Point {
  x: number;
  y: number;
}

// 停止の原因・表示から除外したウィンドウ（ウィンドウのタイトルは含まれない）。
export interface BlockedBy {
  window_id?: string;
  process?: string;
  pid?: number;
  reason?: string;
}

// target メッセージ（#157）。state は none（スコープ付きのリースを持たない）・active・halted。
export interface TargetState {
  state: "none" | "active" | "halted";
  process?: string;
  reason?: string; // halted の理由コード（API 仕様 §3.4）
  reselect?: boolean; // true なら再開できず、選び直しが要る（target_closed / target_replaced / target_unsupported）
  blockedBy?: BlockedBy;
  members: number;
  notices: string[]; // capture_partial は通知（停止ではない）
  excluded: BlockedBy[];
}

export interface StreamGeometry {
  source: StreamSource;
  monitor: number;
  width: number;
  height: number;
  origin?: Point; // グループ源の原点。最初のコマまでは無い
}

export type ServerMessage =
  | ({ type: "ready"; quality: number; fps: number; lease: Lease } & StreamGeometry)
  | ({ type: "config"; quality: number; fps: number } & StreamGeometry)
  | { type: "lease"; lease: Lease }
  | { type: "target"; target: TargetState }
  | { type: "killswitch"; active: boolean }
  | { type: "error"; code: string; message: string; reason?: string }
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

// source は #157 で足した項目。無ければ monitor（古いサーバーとの互換）。
function parseGeometry(m: Record<string, unknown>): StreamGeometry | null {
  if (!isCount(m.monitor) || !isCount(m.width) || !isCount(m.height)) {
    return null;
  }
  const source: StreamSource = m.source === "group" ? "group" : "monitor";
  const o = m.origin;
  const geometry: StreamGeometry = { source, monitor: m.monitor, width: m.width, height: m.height };
  if (isRecord(o) && typeof o.x === "number" && typeof o.y === "number" && Number.isInteger(o.x) && Number.isInteger(o.y)) {
    geometry.origin = { x: o.x, y: o.y };
  }
  return geometry;
}

function parseBlockedBy(value: unknown): BlockedBy | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  const by: BlockedBy = {};
  if (typeof value.window_id === "string") by.window_id = value.window_id;
  if (typeof value.process === "string") by.process = value.process;
  if (typeof value.pid === "number") by.pid = value.pid;
  if (typeof value.reason === "string") by.reason = value.reason;
  return by;
}

function parseTarget(m: Record<string, unknown>): TargetState | null {
  if (m.state !== "none" && m.state !== "active" && m.state !== "halted") {
    return null;
  }
  const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
  const excluded = Array.isArray(m.excluded) ? m.excluded.map(parseBlockedBy).filter((x): x is BlockedBy => x !== undefined) : [];
  return {
    state: m.state,
    process: typeof m.process === "string" ? m.process : undefined,
    reason: typeof m.reason === "string" ? m.reason : undefined,
    reselect: m.reselect === true,
    blockedBy: parseBlockedBy(m.blocked_by),
    members: isCount(m.members) ? m.members : 0,
    notices: strings(m.notices),
    excluded,
  };
}

// parseServerMessage は、JSON をパースした値をサーバーのメッセージとして検証する。形が合わなければ null。
export function parseServerMessage(raw: unknown): ServerMessage | null {
  if (!isRecord(raw)) {
    return null;
  }
  const m = raw;
  switch (m.type) {
    case "ready": {
      const geometry = parseGeometry(m);
      if (geometry && isCount(m.quality) && isCount(m.fps) && isLease(m.lease)) {
        return { type: "ready", ...geometry, quality: m.quality, fps: m.fps, lease: m.lease };
      }
      return null;
    }
    case "config": {
      const geometry = parseGeometry(m);
      if (geometry && isCount(m.quality) && isCount(m.fps)) {
        return { type: "config", ...geometry, quality: m.quality, fps: m.fps };
      }
      return null;
    }
    case "target": {
      const target = parseTarget(m);
      return target ? { type: "target", target } : null;
    }
    case "lease":
      // lease メッセージは lease の内容そのもの（`type` を含む）。
      return isLease(m) ? { type: "lease", lease: { state: m.state, holder: m.holder as LeaseHolder | undefined, mine: m.mine } } : null;
    case "killswitch":
      return typeof m.active === "boolean" ? { type: "killswitch", active: m.active } : null;
    case "error":
      if (typeof m.code === "string" && typeof m.message === "string") {
        return typeof m.reason === "string"
          ? { type: "error", code: m.code, message: m.message, reason: m.reason }
          : { type: "error", code: m.code, message: m.message };
      }
      return null;
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
  // group はグループ源のフレーム（フラグの bit1。monitor は GROUP_MONITOR）。origin は見出しの直後の i32 x・y。
  group: boolean;
  origin: Point | null;
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
const FLAG_GROUP = 2;
const GROUP_ORIGIN_BYTES = 8;

// グループ源のフレームの monitor（実在のモニターの index とは重ならない）。
export const GROUP_MONITOR = 0xffff;

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
  const group = (view.getUint8(1) & FLAG_GROUP) !== 0;
  let origin: Point | null = null;
  let offset = FRAME_HEADER_BYTES;
  if (group) {
    if (buffer.byteLength < FRAME_HEADER_BYTES + GROUP_ORIGIN_BYTES) {
      throw new FrameError("group origin is out of bounds");
    }
    origin = { x: view.getInt32(offset, true), y: view.getInt32(offset + 4, true) };
    offset += GROUP_ORIGIN_BYTES;
  }
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
    group,
    origin,
    monitor: view.getUint16(2, true),
    seq: view.getUint32(4, true),
    width: view.getUint16(8, true),
    height: view.getUint16(10, true),
    tiles,
  };
}
