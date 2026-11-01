// 入力の変換（docs/architecture/frontend.md §6.2「入力」、docs/api/endpoints.md §5.3）。
// 座標・ホイール・キーの変換はコンポーネントと同じディレクトリに置く（共通ライブラリには出さない）。

export type MouseButtonName = "left" | "middle" | "right";

export interface StreamPoint {
  x: number;
  y: number;
}

// サーバーは wheel の dx / dy の絶対値が 50 を超えると 4400 で閉じる。
const MAX_WHEEL_NOTCHES = 50;

// toStreamPoint は、canvas の表示座標（clientX / clientY）をストリームの物理座標へ変換する:
// round((clientX − rect.left) × canvas.width ÷ rect.width)。範囲（0 〜 幅 − 1）へ丸める。表示サイズが 0 なら null。
export function toStreamPoint(
  clientX: number,
  clientY: number,
  rect: { left: number; top: number; width: number; height: number },
  canvasWidth: number,
  canvasHeight: number,
): StreamPoint | null {
  if (rect.width <= 0 || rect.height <= 0 || canvasWidth <= 0 || canvasHeight <= 0) {
    return null;
  }
  const x = Math.round(((clientX - rect.left) * canvasWidth) / rect.width);
  const y = Math.round(((clientY - rect.top) * canvasHeight) / rect.height);
  return { x: clamp(x, 0, canvasWidth - 1), y: clamp(y, 0, canvasHeight - 1) };
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

// mouseButtonName は MouseEvent.button を API のボタン名にする。対応しないボタン（戻る・進む）は null。
export function mouseButtonName(button: number): MouseButtonName | null {
  switch (button) {
    case 0:
      return "left";
    case 1:
      return "middle";
    case 2:
      return "right";
    default:
      return null;
  }
}

const DOM_DELTA_LINE = 1;
const DOM_DELTA_PAGE = 2;

// wheelNotches は、WheelEvent の delta をノッチへ換算する: PIXEL は ÷100、LINE は ÷3、PAGE は ×10。
// 符号は WheelEvent と同じ（dy > 0 が下、dx > 0 が右）。1 メッセージの上限（50）へ丸める。
export function wheelNotches(deltaMode: number, deltaX: number, deltaY: number): { dx: number; dy: number } {
  const scale = deltaMode === DOM_DELTA_PAGE ? 10 : deltaMode === DOM_DELTA_LINE ? 1 / 3 : 1 / 100;
  return { dx: clampNotches(deltaX * scale), dy: clampNotches(deltaY * scale) };
}

function clampNotches(notches: number): number {
  // -0 を 0 にする（JSON は -0 を 0 と書くが、比較しやすくするため）。
  return clamp(notches, -MAX_WHEEL_NOTCHES, MAX_WHEEL_NOTCHES) + 0;
}

// keyModifiers は、サーバーが受け付ける修飾キー名（ctrl / shift / alt / win）にする。Meta は win。
export function keyModifiers(event: Pick<KeyboardEvent, "ctrlKey" | "shiftKey" | "altKey" | "metaKey">): string[] {
  const modifiers: string[] = [];
  if (event.ctrlKey) modifiers.push("ctrl");
  if (event.shiftKey) modifiers.push("shift");
  if (event.altKey) modifiers.push("alt");
  if (event.metaKey) modifiers.push("win");
  return modifiers;
}

// isImeKeyEvent は、IME が処理中のキーイベントか。これらは input.key で送らず、確定した文字列を compositionend で送る。
// Safari は compositionend の直後の keydown に isComposing = false を付けるため、keyCode 229 も見る。
export function isImeKeyEvent(event: Pick<KeyboardEvent, "isComposing" | "keyCode" | "key">): boolean {
  return event.isComposing || event.keyCode === 229 || event.key === "Process";
}
