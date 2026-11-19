// ロゴ（ヘッダーのロックアップ）と favicon を src/brand から dist/brand にコピーする（docs/architecture/frontend.md §8.1）。
// 素材（Nefos Remote の v5c）はそのまま使う。加工・再描画はしない。配信は /static/brand/*。
import { cpSync, mkdirSync } from "node:fs";

mkdirSync("dist", { recursive: true });
cpSync("src/brand", "dist/brand", { recursive: true });
