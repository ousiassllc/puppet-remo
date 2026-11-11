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

  await test.step("ページのスクロールバーは 8px で出ていて、ビューアの画面（remo-screen-viewer）には当たらない", async () => {
    const bar = await page.evaluate(() => ({
      width: window.innerWidth - document.documentElement.clientWidth,
      page: getComputedStyle(document.documentElement, "::-webkit-scrollbar").width,
      viewer: getComputedStyle(document.querySelector("remo-screen-viewer") ?? document.body, "::-webkit-scrollbar").width,
    }));
    expect(bar).toEqual({ width: 8, page: "8px", viewer: "auto" });
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

// ---- ウィンドウ選択・グループ表示・手動操作（#157）----
//
// 偽のデスクトップ（偽アプリ T・メニュー・保存ダイアログ・別プロセスの窓。internal/app/e2e_desktop.go）に対する試験。
// **偽 platform の試験で、Windows 実機の成立性ゲート（S1〜S3。実機未確認。docs/operations/smoke-windows.md §3.10）の代わりにならない。**
// 偽アプリの配置（platformtest.FakeAppLayout）: T は物理 (100,100) の 400x300。合成画像の座標 p の物理座標は 原点 + p。

const T_RECT = { x: 100, y: 100, w: 400, h: 300 };

// 選んだあとの合成画像（T だけ）の大きさ。
const T_IMAGE = { w: 400, h: 300 };

async function canvasSize(canvas: Locator): Promise<{ w: number; h: number }> {
  return canvas.evaluate((c) => (c instanceof HTMLCanvasElement ? { w: c.width, h: c.height } : { w: 0, h: 0 }));
}

async function waitForImage(canvas: Locator, w: number, h: number): Promise<void> {
  await expect.poll(() => canvasSize(canvas)).toEqual({ w, h });
}

// 合成画像の座標 (x, y) へ、canvas の表示の縮尺に直してクリックする（ストリームの座標と同じ変換の逆）。
// マウスの座標は整数（小数は切り捨て）で届き、input.ts は Math.round で画素に直す。canvas の上端・左端は見出しの高さなどで
// 小数になるので、「画素の中心（p + 0.5）」へ寄せる方式は丸めの境目で隣の画素へ倒れる。そこで、画素 p に丸められる範囲
// [p - 0.5, p + 0.5) の最初の整数の座標を選ぶ（縮尺 1 以上なら必ず 1 つある）。クリックでのスクロールで上端が動かないよう、先に表示させる。
async function clickImage(canvas: Locator, x: number, y: number, button: "left" | "right" = "left"): Promise<void> {
  await canvas.scrollIntoViewIfNeeded();
  const box = await canvas.boundingBox();
  const size = await canvasSize(canvas);
  if (!box) {
    throw new Error("canvas has no bounding box");
  }
  const sx = box.width / size.w;
  const sy = box.height / size.h;
  // +0.1: 整数の座標の直上に置き、浮動小数点の誤差で 1 つ手前へ切り捨てられないようにする。
  const px = Math.ceil(box.x + (x - 0.5) * sx) - box.x + 0.1;
  const py = Math.ceil(box.y + (y - 0.5) * sy) - box.y + 0.1;
  await canvas.click({ button, position: { x: px, y: py } });
}

// 一覧の「選ぶ」。承認が要らないクライアントなので、リースがその場で held になる。
async function selectWindow(page: Page, title: string): Promise<void> {
  await page.locator(`[data-window-id]:has([data-window-title]:text-is("${title}")) button`, { hasText: "選ぶ" }).click();
  await expect(page.locator("#window-picker [data-window-scoped]")).toBeVisible();
}

async function openViewer(page: Page): Promise<Locator> {
  await host.identify("registered");
  await page.goto("/viewer");
  await expect(page.locator("#window-picker")).toBeVisible();
  await waitForViewerReady(page.locator("remo-screen-viewer canvas"));
  await trackViewerLease(page);
  return page.locator("remo-screen-viewer canvas");
}

const banner = (page: Page) => page.locator("remo-screen-viewer [data-target-banner]");

// 注入器に届いた、キー・文字・ボタンの呼び出し（カーソルの移動は除く）。
async function keyAndTextCalls(): Promise<string[]> {
  return (await host.inputs()).filter((c) => ["Key", "KeyEvent", "Text"].includes(c.method)).map((c) => c.method);
}

test("[偽 platform の表示] ウィンドウ一覧: 自ウィンドウ・関連ウィンドウは出ず、昇格は灰色で選べない。更新ボタンで再取得できる", async ({ page }) => {
  await openViewer(page);
  const list = page.locator("#window-picker [data-window-list]");
  await expect(list).toBeVisible();
  const titles = await list.locator("[data-window-title]").allTextContents();
  expect(titles).toContain("無題 - FakeNotepad");
  expect(titles).toContain("Notes - e2e-notes.txt");
  // 関連ウィンドウ（保存ダイアログ・メニュー）は一覧に出ない（T を選ぶと自動で対象に入る）。
  expect(titles.join("|")).not.toContain("名前を付けて保存");

  // 昇格は灰色（選べない）。「選ぶ」ボタンが無く、理由を添える。
  const elevated = page.locator('[data-window-id="2002"]');
  await expect(elevated).toHaveAttribute("aria-disabled", "true");
  await expect(elevated.locator("[data-window-note]")).toContainText("管理者権限");
  await expect(elevated.getByRole("button", { name: "選ぶ" })).toHaveCount(0);

  // 手動の更新: 別プロセスの窓が増えると、更新ボタンで一覧に現れる。
  await expect(page.locator('[data-window-id="2003"]')).toHaveCount(0);
  await host.desktop.foreignWindow();
  await page.getByRole("button", { name: "一覧を更新" }).click();
  await expect(page.locator('[data-window-id="2003"] [data-window-title]')).toHaveText("Calculator (e2e)");
});

test("[偽 platform の成功経路。実機の代わりにならない] 選ぶ → メニューバーのメニュー → 項目 → 保存ダイアログ（T の外へ出る）→ ファイル名 → 閉じる。右クリックメニューも同様", async ({ page }) => {
  const canvas = await openViewer(page);

  await test.step("T を選ぶと、T だけの合成画像になり、停止バナーは出ない", async () => {
    await selectWindow(page, "無題 - FakeNotepad");
    await waitForViewerLease(page, "held", true);
    await waitForImage(canvas, T_IMAGE.w, T_IMAGE.h);
    await expect(banner(page)).toHaveCount(0);
    // スコープ中は送信メニューの Alt+Tab・Win+D が無効、クリップボードの読み取りの UI が無い。
    await expect(page.locator('#key-menu button[data-key-disabled]')).toHaveCount(2);
    await expect(page.locator('#clipboard-panel [data-clipboard-write-only]')).toBeVisible();
    await expect(page.getByRole("button", { name: "ホストのクリップボードを取得" })).toHaveCount(0);
  });

  await test.step("メニューバーの「ファイル」→ 項目をクリックすると、保存ダイアログが開き、画像が広がる（T の外へはみ出す）", async () => {
    await clickImage(canvas, 25, 15); // 物理 (125,115) = 「ファイル」
    await expect.poll(async () => (await host.desktop.app()).menu).toBe("file");
    await clickImage(canvas, 80, 37); // 物理 (180,137) = メニューの項目
    await expect.poll(async () => (await host.desktop.app()).dialog).toBe(true);
    // ダイアログ (450..650, 80..200) が T (100..500, 100..400) の外へ出る: 合成画像は (100,80)〜(650,400) の 550x320。
    await waitForImage(canvas, 550, 320);
    await expect(banner(page)).toHaveCount(0);
  });

  await test.step("ファイル名を入力して Enter で保存する（画像は T だけに戻る）", async () => {
    await clickImage(canvas, 400, 40); // 物理 (500,120) = ダイアログの本文（フォーカスを入力欄へ移す）
    await page.locator("remo-screen-viewer textarea").evaluate((el) => {
      el.dispatchEvent(new CompositionEvent("compositionend", { data: "memo.txt" }));
    });
    await page.keyboard.press("Enter");
    await expect.poll(async () => (await host.desktop.app()).saved).toEqual(["memo.txt"]);
    await waitForImage(canvas, T_IMAGE.w, T_IMAGE.h);
    expect((await host.desktop.app()).dialog).toBe(false);
  });

  await test.step("T の端で右クリックすると、右クリックメニューが T の外へ出て、項目のクリックで完了する", async () => {
    await clickImage(canvas, 380, 280, "right"); // 物理 (480,380)。メニュー (480..600, 380..440) が T の右下の外へ出る
    await expect.poll(async () => (await host.desktop.app()).menu).toBe("context");
    await waitForImage(canvas, 500, 340);
    await clickImage(canvas, 400, 290); // 物理 (500,390) = 上半分「すべて選択」
    await expect.poll(async () => (await host.desktop.app()).selected).toBe(true);
    await waitForImage(canvas, T_IMAGE.w, T_IMAGE.h);
  });

  await test.step("注入された座標は、合成画像の座標 + 原点（物理座標）で、停止していない", async () => {
    const moves = (await host.inputs()).filter((c) => c.method === "MoveTo").map((c) => asPoint(c.args[0]));
    expect(moves).toContainEqual({ x: 125, y: 115 });
    expect(moves).toContainEqual({ x: 180, y: 137 });
    expect(moves).toContainEqual({ x: 480, y: 380 });
    await expect(banner(page)).toHaveCount(0);
    await expect(page.locator("remo-screen-viewer .overlay")).toHaveCount(0);
  });
});

test("[安全停止] 別プロセスの偽ダイアログが前面に出ると、以後キー・文字は届かず halted（Esc も送らない）。再開は明示的で、原因が残っていれば halted のまま", async ({ page }) => {
  const canvas = await openViewer(page);
  await host.desktop.crossProcessDialog(true);
  await selectWindow(page, "無題 - FakeNotepad");
  await waitForImage(canvas, T_IMAGE.w, T_IMAGE.h);
  await clickImage(canvas, 25, 15);
  await expect.poll(async () => (await host.desktop.app()).menu).toBe("file");
  await clickImage(canvas, 80, 37);
  await expect.poll(async () => (await host.desktop.app()).dialog).toBe(true);

  await test.step("前面のダイアログへのキーは注入されず、停止バナーが出る", async () => {
    await page.keyboard.press("a");
    await expect(banner(page)).toContainText("別のアプリのダイアログが前面");
    await expect(banner(page)).toHaveAttribute("data-target-tone", "halted");
    expect(await keyAndTextCalls()).toEqual([]);
  });

  await test.step("停止中の Esc・クリック・文字は送られない。「再開」は再検証だけで、原因が残るので halted のまま", async () => {
    await page.keyboard.press("Escape");
    await clickImage(canvas, 25, 15);
    await page.getByRole("button", { name: "再開" }).click();
    // 再開の要求はこの接続で先の入力のあとに処理される: 結果（halted のまま）が届いてから、注入が 0 であることを確かめる。
    await expect(banner(page)).toContainText("別のアプリのダイアログが前面");
    expect(await keyAndTextCalls()).toEqual([]);
    expect((await host.desktop.app()).dialog).toBe(true);
    expect((await host.inputs()).filter((c) => c.method === "Button")).toHaveLength(4); // 保存ダイアログを開くまでの 2 クリック（down・up）だけ
  });
});

test("[安全停止] 非メンバーが前面に出ると halted。ホスト側で退かしても自動では戻らず、「再開」で再検証されて active に戻る", async ({ page }) => {
  const canvas = await openViewer(page);
  await selectWindow(page, "無題 - FakeNotepad");
  await waitForImage(canvas, T_IMAGE.w, T_IMAGE.h);

  await host.desktop.foreignWindow(); // T の右半分を覆う前面のウィンドウ
  // 覆われていない座標（T の左側）のクリックは入る（座標の最上位は T）。
  await clickImage(canvas, 10, 200);
  await expect.poll(async () => (await host.inputs()).filter((c) => c.method === "Button").length).toBe(2);
  await expect(banner(page)).toHaveCount(0);
  // キーの宛先（前面）は別プロセスのウィンドウなので、注入されず停止する。
  await page.keyboard.press("a");
  await expect(banner(page)).toHaveAttribute("data-target-tone", "halted");
  await expect(banner(page)).toContainText("対象と関係のないウィンドウが前面");
  expect(await keyAndTextCalls()).toEqual([]);
  const clicks = (await host.inputs()).filter((c) => c.method === "Button").length;

  await host.desktop.removeForeignWindow();
  // ホスト側で原因を取り除いても、自動では戻らない（バナーは出たまま、入力は届かない）。
  await clickImage(canvas, 10, 200);
  await expect(banner(page)).toHaveAttribute("data-target-tone", "halted");
  expect((await host.inputs()).filter((c) => c.method === "Button")).toHaveLength(clicks);

  await page.getByRole("button", { name: "再開" }).click();
  await expect(banner(page)).toHaveCount(0);
  await clickImage(canvas, 25, 15);
  await expect.poll(async () => (await host.desktop.app()).menu).toBe("file");
});

test("[安全停止。成功条件ではない] メニューが撮れない設定 → capture_member_unavailable で halted、「対象を表示できません」。画面全体には切り替わらない", async ({ page }) => {
  const canvas = await openViewer(page);
  await host.desktop.unprintable("menu", true);
  await selectWindow(page, "無題 - FakeNotepad");
  await waitForImage(canvas, T_IMAGE.w, T_IMAGE.h);

  await clickImage(canvas, 25, 15); // メニューが開く（偽アプリは開く）→ 撮れないので停止
  await expect(banner(page)).toContainText("関連するメニュー・ダイアログを表示できない");
  await expect(page.locator("remo-screen-viewer .overlay")).toContainText("対象を表示できません");
  // 以後の入力は届かない。画像の大きさは、画面全体（640x480）にならない。
  const before = (await host.inputs()).length;
  await page.keyboard.press("a"); // 表示できないあいだ、canvas は覆われる（クリックは届かない）。キーは送られない
  await page.getByRole("button", { name: "再開" }).click();
  await expect(banner(page)).toContainText("関連するメニュー・ダイアログを表示できない");
  expect((await host.inputs()).length).toBe(before);
  expect(await canvasSize(canvas)).not.toEqual({ w: 640, h: 480 });
});

test("[偽 platform の表示] 対象を閉じると、選び直しのみ（「再開」なし）。「別のウィンドウを選ぶ」で解放され、一覧に戻る", async ({ page }) => {
  const canvas = await openViewer(page);
  await selectWindow(page, "無題 - FakeNotepad");
  await waitForImage(canvas, T_IMAGE.w, T_IMAGE.h);

  await host.desktop.closeTarget();
  await expect(banner(page)).toContainText("対象のウィンドウが閉じられました");
  await expect(banner(page).getByRole("button", { name: "再開" })).toHaveCount(0);

  await banner(page).getByRole("button", { name: "別のウィンドウを選ぶ" }).click();
  await expect(page.locator("#window-picker [data-window-list]")).toBeVisible();
  await expect(banner(page)).toHaveCount(0);
  await waitForViewerLease(page, "idle", false);
  // モニター表示（640x480）に戻っている。
  await waitForViewerReady(canvas);
});

test("[偽 platform の表示] 「モニター表示」「ウィンドウ表示」で、スコープ付きのリースを保ったまま画面全体と対象を行き来できる（モニターが 1 台でも）", async ({ page }) => {
  const canvas = await openViewer(page);
  await selectWindow(page, "無題 - FakeNotepad");
  await waitForImage(canvas, T_IMAGE.w, T_IMAGE.h);

  await page.getByRole("button", { name: "モニター表示" }).click();
  await waitForImage(canvas, STREAM_WIDTH, STREAM_HEIGHT); // 画面全体（権限として残る。D1）
  await expect(page.locator("#window-picker [data-window-scoped]")).toBeVisible(); // リースは保たれている

  await page.getByRole("button", { name: "ウィンドウ表示（選択中の対象に戻る）" }).click();
  await waitForImage(canvas, T_IMAGE.w, T_IMAGE.h);
  await clickImage(canvas, 25, 15);
  await expect.poll(async () => (await host.desktop.app()).menu).toBe("file");

  // 解放するとボタンは消え、モニター表示に戻る。
  await page.getByRole("button", { name: "解放" }).click();
  await expect(page.getByRole("button", { name: "モニター表示" })).toHaveCount(0);
  await waitForImage(canvas, STREAM_WIDTH, STREAM_HEIGHT);
});
