// input.ts のテスト（座標・ホイール・キーの変換。docs/api/endpoints.md §5.3、frontend.md §6.2）。
import { expect } from "@open-wc/testing";
import { isImeKeyEvent, keyModifiers, mouseButtonName, toStreamPoint, wheelNotches } from "./input";

describe("toStreamPoint", () => {
  // 表示は 320 × 180、ストリームは 1920 × 1080（6 倍）、表示の左上は (100, 50)。
  const rect = { left: 100, top: 50, width: 320, height: 180 };

  it("round((clientX − rect.left) × canvas.width ÷ rect.width) で変換する", () => {
    expect(toStreamPoint(100, 50, rect, 1920, 1080)).to.deep.equal({ x: 0, y: 0 });
    expect(toStreamPoint(260, 140, rect, 1920, 1080)).to.deep.equal({ x: 960, y: 540 });
    expect(toStreamPoint(150, 80, rect, 1920, 1080)).to.deep.equal({ x: 300, y: 180 });
  });

  it("端数は四捨五入する", () => {
    // 表示 3 px → ストリーム 10 px: 1 px = 3.33…、2 px = 6.66…
    const small = { left: 0, top: 0, width: 3, height: 3 };
    expect(toStreamPoint(1, 1, small, 10, 10)).to.deep.equal({ x: 3, y: 3 });
    expect(toStreamPoint(2, 2, small, 10, 10)).to.deep.equal({ x: 7, y: 7 });
    // .5 は切り上げ（Math.round）。
    expect(toStreamPoint(1, 1, { left: 0, top: 0, width: 4, height: 4 }, 10, 10)).to.deep.equal({ x: 3, y: 3 });
  });

  it("範囲（0 〜 幅 − 1）へ丸める", () => {
    expect(toStreamPoint(0, 0, rect, 1920, 1080)).to.deep.equal({ x: 0, y: 0 });
    expect(toStreamPoint(10_000, 10_000, rect, 1920, 1080)).to.deep.equal({ x: 1919, y: 1079 });
  });

  it("表示サイズかストリームのサイズが 0 以下なら null", () => {
    expect(toStreamPoint(1, 1, { left: 0, top: 0, width: 0, height: 10 }, 10, 10)).to.equal(null);
    expect(toStreamPoint(1, 1, { left: 0, top: 0, width: 10, height: 0 }, 10, 10)).to.equal(null);
    expect(toStreamPoint(1, 1, rect, 0, 10)).to.equal(null);
    expect(toStreamPoint(1, 1, rect, 10, 0)).to.equal(null);
  });
});

describe("mouseButtonName", () => {
  it("0 / 1 / 2 を left / middle / right にし、戻る・進むは null", () => {
    expect([0, 1, 2, 3, 4].map(mouseButtonName)).to.deep.equal(["left", "middle", "right", null, null]);
  });
});

describe("wheelNotches", () => {
  const DOM_DELTA_PIXEL = 0;
  const DOM_DELTA_LINE = 1;
  const DOM_DELTA_PAGE = 2;

  it("PIXEL は ÷100", () => {
    expect(wheelNotches(DOM_DELTA_PIXEL, 100, 200)).to.deep.equal({ dx: 1, dy: 2 });
    expect(wheelNotches(DOM_DELTA_PIXEL, 0, 50)).to.deep.equal({ dx: 0, dy: 0.5 });
  });

  it("LINE は ÷3", () => {
    expect(wheelNotches(DOM_DELTA_LINE, 3, 6)).to.deep.equal({ dx: 1, dy: 2 });
  });

  it("PAGE は ×10", () => {
    expect(wheelNotches(DOM_DELTA_PAGE, 1, 2)).to.deep.equal({ dx: 10, dy: 20 });
  });

  it("符号は WheelEvent のまま（dy > 0 が下、dx > 0 が右）", () => {
    expect(wheelNotches(DOM_DELTA_PIXEL, -100, -300)).to.deep.equal({ dx: -1, dy: -3 });
  });

  it("1 メッセージの上限（絶対値 50）へ丸め、-0 は 0 にする", () => {
    expect(wheelNotches(DOM_DELTA_PAGE, 100, -100)).to.deep.equal({ dx: 50, dy: -50 });
    const { dx } = wheelNotches(DOM_DELTA_PIXEL, -0, 0);
    expect(Object.is(dx, 0)).to.equal(true);
  });
});

describe("keyModifiers", () => {
  it("ctrl / shift / alt / win（Meta）の順に並べる", () => {
    expect(keyModifiers({ ctrlKey: true, shiftKey: true, altKey: true, metaKey: true })).to.deep.equal(["ctrl", "shift", "alt", "win"]);
    expect(keyModifiers({ ctrlKey: false, shiftKey: false, altKey: true, metaKey: true })).to.deep.equal(["alt", "win"]);
    expect(keyModifiers({ ctrlKey: false, shiftKey: false, altKey: false, metaKey: false })).to.deep.equal([]);
  });
});

describe("isImeKeyEvent", () => {
  it("isComposing・keyCode 229・key Process のどれかなら IME のイベント", () => {
    expect(isImeKeyEvent({ isComposing: true, keyCode: 65, key: "a" })).to.equal(true);
    expect(isImeKeyEvent({ isComposing: false, keyCode: 229, key: "a" })).to.equal(true);
    expect(isImeKeyEvent({ isComposing: false, keyCode: 65, key: "Process" })).to.equal(true);
    expect(isImeKeyEvent({ isComposing: false, keyCode: 65, key: "a" })).to.equal(false);
  });
});
