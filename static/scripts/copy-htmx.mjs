// htmx 本体と拡張（response-targets・sse）を dist/js にコピーする（docs/architecture/frontend.md §8.1）。
// 版は bun.lock で固定する。
import { copyFileSync, mkdirSync } from "node:fs";

const files = [
  ["node_modules/htmx.org/dist/htmx.min.js", "dist/js/htmx.min.js"],
  ["node_modules/htmx-ext-response-targets/dist/response-targets.min.js", "dist/js/response-targets.min.js"],
  ["node_modules/htmx-ext-sse/dist/sse.min.js", "dist/js/sse.min.js"],
];

mkdirSync("dist/js", { recursive: true });
for (const [from, to] of files) {
  copyFileSync(from, to);
}
