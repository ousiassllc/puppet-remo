// リモートの Web UI の E2E（docs/architecture/frontend.md §9）。テスト専用サーバー（偽の platform・偽の tailnet）に対して、
// 実際の Chromium で操作する。ホスト側の操作（許可・強制剥奪・キルスイッチ）は制御エンドポイント（helpers.ts の host）から行う。
// 待ち合わせは Playwright の自動待機・web-first アサーション・expect.poll だけで、固定時間の待機は使わない。
import type { Locator, Page } from "@playwright/test";
import { asPoint, expect, host, test } from "./helpers";

// ストリームの座標空間は、偽のキャプチャの 640x480。
const STREAM_WIDTH = 640;
const STREAM_HEIGHT = 480;

test.beforeEach(async () => {
  await host.reset();
});

// <remo-screen-viewer> が WebSocket の ready を受けて接続できた（canvas がストリームのサイズになった）ことを待つ。
async function waitForViewerReady(canvas: Locator): Promise<void> {
  await expect.poll(() => canvas.evaluate((c) => (c instanceof HTMLCanvasElement ? c.width : 0))).toBe(STREAM_WIDTH);
}

// ビューアが WebSocket の lease を受け取った最新の内容を window に残す（入力が有効になる時点を、UI の外から観測するため）。
// 操作権を取得したあと、ビューアが mine: true を受け取るまでクリックを待たせる。
async function trackViewerLease(page: Page): Promise<void> {
  await page.evaluate(() => {
    Reflect.set(window, "__lease", undefined);
    document.addEventListener("remo:lease-changed", (e) => {
      if (e instanceof CustomEvent) {
        Reflect.set(window, "__lease", e.detail);
      }
    });
  });
}

async function waitForViewerLease(page: Page, state: string, mine: boolean): Promise<void> {
  await page.waitForFunction(
    ([wantState, wantMine]) => {
      const lease = Reflect.get(window, "__lease");
      return lease?.state === wantState && lease.mine === wantMine;
    },
    [state, mine] as const,
  );
}

test("未登録デバイスの待機 → 許可 → ビューア → 操作権 → クリック → 強制剥奪 → キルスイッチ", async ({ page }) => {
  await host.identify("unregistered");

  await test.step("未登録のデバイスは待機ページ（接続要求が作られる）", async () => {
    const res = await page.goto("/viewer");
    expect(res?.status()).toBe(403);
    await expect(page.locator('main[data-connection-state="pending"]')).toBeVisible();
    await expect(page.getByRole("heading", { name: "接続要求を送信しました" })).toBeVisible();
    await expect.poll(async () => (await host.pendingRequests()).map((r) => r.display_name)).toEqual(["e2e-phone"]);
  });

  await test.step("ホストが許可すると、再読み込みでビューアが開く", async () => {
    await host.allow();
    const res = await page.reload();
    expect(res?.status()).toBe(200);
    await expect(page.locator("remo-screen-viewer")).toBeVisible();
    await expect(page.locator('#lease-panel [data-lease-state="idle"]')).toBeVisible();
    await waitForViewerReady(page.locator("remo-screen-viewer canvas"));
    await expect(page.locator("remo-screen-viewer .overlay")).toHaveCount(0);
  });

  const canvas = page.locator("remo-screen-viewer canvas");
  await trackViewerLease(page);

  await test.step("操作権を取得すると、ビューアの入力が有効になる", async () => {
    await page.getByRole("button", { name: "操作権を取得" }).click();
    await expect(page.locator('#lease-panel [data-lease-state="mine"]')).toBeVisible();
    await expect(page.getByRole("button", { name: "解放" })).toBeVisible();
    await waitForViewerLease(page, "held", true);
  });

  await test.step("キャンバスのクリックが偽の injector に届く（座標とボタン）", async () => {
    const box = await canvas.boundingBox();
    if (!box) {
      throw new Error("canvas has no bounding box");
    }
    const at = { x: 100, y: 60 };
    await canvas.click({ position: at });
    // 座標の変換（docs §6.2）: round((クライアント座標 − rect の左上) × canvas の幅 ÷ rect の幅）。表示の縮小の端数で 1 ずれうる。
    const wantX = Math.round((at.x * STREAM_WIDTH) / box.width);
    const wantY = Math.round((at.y * STREAM_HEIGHT) / box.height);

    await expect
      .poll(async () => {
        const calls = await host.inputs();
        return calls.filter((c) => c.method === "Button").map((c) => c.args);
      })
      .toEqual([
        ["left", "down"],
        ["left", "up"],
      ]);
    const moves = (await host.inputs()).filter((c) => c.method === "MoveTo").map((c) => c.args[0]);
    expect(moves.length).toBeGreaterThan(0);
    for (const arg of moves) {
      const { x, y } = asPoint(arg);
      expect(Math.abs(x - wantX)).toBeLessThanOrEqual(1);
      expect(Math.abs(y - wantY)).toBeLessThanOrEqual(1);
    }
  });

  await test.step("ホストが強制剥奪すると、画面の操作権の表示が空きに戻る", async () => {
    await host.revokeLease();
    await expect(page.locator('#lease-panel [data-lease-state="idle"]')).toBeVisible();
    await expect(page.getByRole("button", { name: "操作権を取得" })).toBeVisible();
    await waitForViewerLease(page, "idle", false);
  });

  await test.step("取得し直したあとキルスイッチを発動すると、ビューアが停止し、再読み込みは 503", async () => {
    await trackViewerLease(page);
    await page.getByRole("button", { name: "操作権を取得" }).click();
    await waitForViewerLease(page, "held", true);

    await host.triggerKillSwitch();
    await expect(page.locator("remo-screen-viewer .overlay")).toContainText("キルスイッチ");

    const res = await page.reload();
    expect(res?.status()).toBe(503);
    await expect(page.locator('main[data-error-code="kill_switch_active"]')).toBeVisible();
  });
});

test("登録済みのデバイスは許可なしでビューアが開く", async ({ page }) => {
  await host.identify("registered");
  const res = await page.goto("/viewer");
  expect(res?.status()).toBe(200);
  await expect(page.locator('#lease-panel [data-lease-state="idle"]')).toBeVisible();
  await waitForViewerReady(page.locator("remo-screen-viewer canvas"));
});

// 監視が黙って壊れていないことの確認: ページのスクリプトを CSP が許さない形（インラインのスクリプト）で足すと、
// securitypolicyviolation が検出される。ここでは検出を確かめたあとに記録を空にして、テストを通す。
test("CSP 違反の監視が、インラインのスクリプトの違反を検出する", async ({ page, cspViolations }) => {
  await host.identify("registered");
  await page.goto("/viewer");
  await expect(page.locator("remo-screen-viewer")).toBeVisible();

  await page.evaluate(() => {
    const script = document.createElement("script");
    script.textContent = "window.__inlineScriptRan = true;";
    document.head.appendChild(script);
  });

  await expect.poll(() => cspViolations.length).toBeGreaterThan(0);
  expect(await page.evaluate(() => Reflect.get(window, "__inlineScriptRan"))).toBeUndefined();
  cspViolations.splice(0);
});
