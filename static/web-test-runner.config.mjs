// Lit のテストの設定（docs/architecture/frontend.md §8.2・§9）。
// - テストは src/ の *.test.ts。esbuild のプラグインで TypeScript をそのまま読み込む（バンドルしない。`bun run build` の対象外）。
// - ブラウザは Playwright の Chromium（ヘッドレス）。PLAYWRIGHT_BROWSERS_PATH があればその下の Chromium を使う。
// - lib/logger.ts の __DEBUG__ は、本番ビルド（esbuild.config.mjs）と同じく false にする。
import { esbuildPlugin } from "@web/dev-server-esbuild";
import { playwrightLauncher } from "@web/test-runner-playwright";

export default {
  files: "src/**/*.test.ts",
  nodeResolve: true,
  plugins: [esbuildPlugin({ ts: true, target: "es2022", define: { __DEBUG__: "false" } })],
  browsers: [playwrightLauncher({ product: "chromium" })],
};
