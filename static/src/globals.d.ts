// esbuild の define（esbuild.config.mjs）が置き換えるビルド時の定数。
// 開発ビルド（--watch）で true。本番ビルドは false（lib/logger.ts がデバッグ・情報のログを出さない）。
declare const __DEBUG__: boolean;
