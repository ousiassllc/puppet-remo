// Issue #247 のスクリーンショットを撮る（偽 service のデモ。Windows 実機の代わりではない）。README.md の手順を参照。
//
//   node docs/screenshots/issue-247/capture.mjs <guidemo のバイナリ> [出力先ディレクトリ]
//
// Playwright は static/node_modules のものを使い、ブラウザは PLAYWRIGHT_CHROMIUM（任意。実行ファイルのパス）か、
// Playwright が見つけたものを使う。guidemo は状態ごとに loopback の空きポートで起動して、撮り終えたら止める。
import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(path.resolve(here, "../../../static/package.json"));
const { chromium } = require("playwright");

const guidemo = path.resolve(process.argv[2] ?? "");
const outDir = path.resolve(process.argv[3] ?? here);

// 1 枚 = 1 つの guidemo の状態 + 画面の操作。card は撮る位置（その要素を画面の上端へ）。
const shots = [
  { file: "agent-chatgpt-signed-out.png", state: "chatgpt_signed_out", card: "#chatgpt-card" },
  { file: "agent-chatgpt-connected.png", state: "chatgpt_connected", card: "#chatgpt-card" },
  { file: "agent-chatgpt-connected-dark.png", state: "chatgpt_connected", card: "#chatgpt-card", scheme: "dark" },
  { file: "agent-chatgpt-usage-limited.png", state: "chatgpt_usage_limited", card: "#chatgpt-card" },
  { file: "agent-chatgpt-policy-denied.png", state: "chatgpt_policy_denied", card: "#chatgpt-card" },
  { file: "agent-codex-card.png", state: "codex_connected", card: "#codex-card" },
  { file: "agent-codex-installing.png", state: "codex_installing", card: "#codex-card" },
  { file: "agent-chatgpt-signout-modal.png", state: "chatgpt_connected", card: "#chatgpt-card", click: "[hx-get='/agent/chatgpt/signout/confirm']", modal: true },
  { file: "agent-api-key-switch-modal.png", state: "chatgpt_usage_limited", card: "#chatgpt-card", click: "[hx-get^='/agent/connection/api-key/confirm']", modal: true },
];

let nextPort = 18900 + Math.floor(Math.random() * 500);

async function startDemo(state) {
  const addr = `127.0.0.1:${nextPort++}`;
  const proc = spawn(guidemo, ["-addr", addr, "-agent", state], { stdio: ["ignore", "ignore", "pipe"] });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`guidemo (${state}) did not start`)), 15000);
    proc.stderr.on("data", (b) => {
      if (String(b).includes("guidemo: http://")) {
        clearTimeout(timer);
        resolve();
      }
    });
    proc.on("exit", (code) => reject(new Error(`guidemo (${state}) exited: ${code}`)));
  });
  return { url: `http://${addr}`, stop: () => proc.kill("SIGINT") };
}

const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_CHROMIUM || undefined });
try {
  for (const s of shots) {
    const demo = await startDemo(s.state);
    try {
      const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: s.scheme ?? "light", reducedMotion: "reduce" });
      const page = await ctx.newPage();
      await page.goto(`${demo.url}/agent`);
      await page.waitForSelector(s.card);
      if (s.click) {
        await page.locator(s.click).first().click();
        await page.waitForSelector("#modal");
      }
      await page.locator(s.card).evaluate((el, block) => el.scrollIntoView({ block }), s.modal ? "center" : "start");
      if (!s.modal) await page.evaluate(() => window.scrollBy(0, -16));
      // 出現のアニメーションが終わってから撮る。
      await page.evaluate(() => Promise.all(document.getAnimations().filter((a) => a.effect.getComputedTiming().iterations !== Infinity).map((a) => a.finished)));
      await page.screenshot({ path: path.join(outDir, s.file) });
      console.log("wrote", s.file);
      await ctx.close();
    } finally {
      demo.stop();
    }
  }
} finally {
  await browser.close();
}
