// dist を空にする（ビルドを再現可能にするため、毎回作り直す）。
import { rmSync } from "node:fs";

rmSync("dist", { recursive: true, force: true });
