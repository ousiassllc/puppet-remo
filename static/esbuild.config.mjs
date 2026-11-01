// esbuild のビルド設定（docs/architecture/frontend.md §8.1）。
// - htmx-config.ts: IIFE（classic スクリプト。htmx の直後に同期で読み込む）
// - app.ts: ES モジュール
// - components/screen-viewer: ES モジュール（Lit の Web Component。出力名は screen-viewer.js。レイアウトが type=module で読み込む）
// 本番は minify、--watch のときは sourcemap と、`wails3 dev` の起動前確認のための serve（§8.3）。
import * as esbuild from "esbuild";

const watch = process.argv.includes("--watch");

const common = {
  bundle: true,
  outdir: "dist/js",
  target: "es2022",
  logLevel: "info",
  minify: !watch,
  sourcemap: watch,
  // lib/logger.ts が本番でデバッグ・情報のログを出さないための定数（src/globals.d.ts）。
  define: { __DEBUG__: String(watch) },
};

const builds = [
  { ...common, entryPoints: ["src/app/htmx-config.ts"], format: "iife" },
  { ...common, entryPoints: ["src/app/app.ts"], format: "esm" },
  {
    ...common,
    entryPoints: { "screen-viewer": "src/components/screen-viewer/remo-screen-viewer.ts" },
    format: "esm",
  },
];

if (!watch) {
  await Promise.all(builds.map((b) => esbuild.build(b)));
} else {
  const contexts = await Promise.all(builds.map((b) => esbuild.context(b)));
  await Promise.all(contexts.map((c) => c.watch()));
  // wails3 dev は WAILS_VITE_PORT（既定 9245）で待ち受ける開発サーバーを前提にする。
  // この serve は待ち受けの確認とライブのリビルド用のスタブで、GUI のページは Gin ではなく Go のハンドラが配信する。
  const port = Number(process.env.WAILS_VITE_PORT ?? 9245);
  await contexts[0].serve({ servedir: "dist", port });
  console.log(`esbuild: watching, serving dist on :${port}`);
}
