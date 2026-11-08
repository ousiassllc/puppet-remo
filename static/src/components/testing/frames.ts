// テスト用の補助: バイナリフレームの組み立て（docs/api/endpoints.md §5.4。リトルエンディアン）、
// 実物の JPEG タイルの生成、canvas の画素の検査、上限付きの条件待ち。

export interface TestTile {
  x: number;
  y: number;
  w: number;
  h: number;
  jpeg: Uint8Array;
}

export interface TestFrame {
  keyframe: boolean;
  monitor?: number;
  // origin を指定するとグループ源のフレーム（フラグ bit1、monitor 0xffff、見出しの後ろに i32 の原点）にする。
  origin?: { x: number; y: number };
  seq: number;
  width: number;
  height: number;
  tiles: TestTile[];
}

// buildFrame は、API 仕様 §5.4 のバイナリフレームを作る。
export function buildFrame(frame: TestFrame): ArrayBuffer {
  const originBytes = frame.origin ? 8 : 0;
  const size = 14 + originBytes + frame.tiles.reduce((sum, tile) => sum + 12 + tile.jpeg.length, 0);
  const buffer = new ArrayBuffer(size);
  const view = new DataView(buffer);
  view.setUint8(0, 1);
  view.setUint8(1, (frame.keyframe ? 1 : 0) | (frame.origin ? 2 : 0));
  view.setUint16(2, frame.origin ? 0xffff : (frame.monitor ?? 0), true);
  view.setUint32(4, frame.seq, true);
  view.setUint16(8, frame.width, true);
  view.setUint16(10, frame.height, true);
  view.setUint16(12, frame.tiles.length, true);
  let offset = 14;
  if (frame.origin) {
    view.setInt32(offset, frame.origin.x, true);
    view.setInt32(offset + 4, frame.origin.y, true);
    offset += 8;
  }
  for (const tile of frame.tiles) {
    view.setUint16(offset, tile.x, true);
    view.setUint16(offset + 2, tile.y, true);
    view.setUint16(offset + 4, tile.w, true);
    view.setUint16(offset + 6, tile.h, true);
    view.setUint32(offset + 8, tile.jpeg.length, true);
    new Uint8Array(buffer, offset + 12, tile.jpeg.length).set(tile.jpeg);
    offset += 12 + tile.jpeg.length;
  }
  return buffer;
}

export type ColorName = "red" | "green" | "blue";

const colorStyles: Record<ColorName, string> = { red: "#ff0000", green: "#00ff00", blue: "#0000ff" };

// solidJpeg は、単色（赤・緑・青）の JPEG を、ブラウザの JPEG エンコーダーで作る（画面のタイルの代わり）。
export async function solidJpeg(color: ColorName, w: number, h: number): Promise<Uint8Array> {
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d");
  if (!ctx) {
    throw new Error("2d context is not available");
  }
  ctx.fillStyle = colorStyles[color];
  ctx.fillRect(0, 0, w, h);
  const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", 0.95));
  if (!blob) {
    throw new Error("cannot encode a JPEG");
  }
  return new Uint8Array(await blob.arrayBuffer());
}

// solidTile は、単色のタイル（x, y に置く w × h）。
export async function solidTile(color: ColorName, x: number, y: number, w: number, h: number): Promise<TestTile> {
  return { x, y, w, h, jpeg: await solidJpeg(color, w, h) };
}

// classifyPixel は、canvas の画素（x, y）が、赤・緑・青のどれか、または描かれていない（透明）かを返す。JPEG は不可逆なので許容幅を持つ。
export function classifyPixel(canvas: HTMLCanvasElement, x: number, y: number): ColorName | "transparent" | "other" {
  const ctx = canvas.getContext("2d");
  if (!ctx) {
    throw new Error("2d context is not available");
  }
  const [r = 0, g = 0, b = 0, a = 0] = ctx.getImageData(x, y, 1, 1).data;
  if (a === 0) {
    return "transparent";
  }
  if (r > 180 && g < 80 && b < 80) return "red";
  if (g > 180 && r < 80 && b < 80) return "green";
  if (b > 180 && r < 80 && g < 80) return "blue";
  return "other";
}

// until は、条件が真になるまでアニメーションフレームごとに確かめる、上限付きの条件待ち（固定時間の待ちではない。
// setTimeout を使わないので、偽のタイマーを入れたテストでも使える）。
export async function until(condition: () => boolean, description: string, maxFrames = 600): Promise<void> {
  for (let i = 0; i < maxFrames; i++) {
    if (condition()) {
      return;
    }
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  }
  throw new Error(`condition was not met: ${description}`);
}
