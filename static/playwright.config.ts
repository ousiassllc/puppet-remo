// Playwright の E2E の設定（docs/architecture/frontend.md §9）。Chromium だけ。
// テスト専用サーバー（`-tags e2e` の cmd/puppet-remo-e2e。偽の platform・偽の tailnet・loopback の待ち受け）は webServer が
// ビルドして起動し、制御エンドポイントの GET /ready が 200 になるのを待つ。事前に `bun run build`（static/dist）が要る。
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, devices } from "@playwright/test";
import { controlPort, remotePort, remoteURL } from "./e2e/env";

const staticDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(staticDir, "..");
const serverBin = path.join(os.tmpdir(), "puppet-remo-e2e-server");

export default defineConfig({
  testDir: "./e2e",
  outputDir: "./test-results",
  // テスト専用サーバーの状態（リース・キルスイッチ・接続要求）は 1 つなので、直列に流す。各テストは先頭でサーバーを初期化する。
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: process.env.CI ? [["list"], ["github"]] : "list",
  use: {
    baseURL: remoteURL,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    // 一度ビルドしてから実行する（`go run` は SIGTERM を子へ渡さず、一時ディレクトリが残るため）。
    command: `go build -tags e2e -o ${JSON.stringify(serverBin)} ./cmd/puppet-remo-e2e && ${JSON.stringify(serverBin)} -port ${remotePort} -control 127.0.0.1:${controlPort}`,
    cwd: repoRoot,
    env: { PUPPET_REMO_STATIC_DIR: path.join(staticDir, "dist") },
    url: `http://127.0.0.1:${controlPort}/ready`,
    // 前の実行のサーバーが残っていたら、古いビルドで走らせずに失敗させる。
    reuseExistingServer: false,
    // サーバーは SIGTERM で一時ディレクトリ（SQLite）を消して終わる。
    gracefulShutdown: { signal: "SIGTERM", timeout: 10_000 },
    stderr: "pipe",
    timeout: 240_000,
  },
});
