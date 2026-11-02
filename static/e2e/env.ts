// E2E の接続先。playwright.config.ts（テスト専用サーバーの起動）と、テスト・ヘルパーが同じ値を使う。
// リモートルーターのポートは、サーバーの設定 server.port（Host 検査のポート）と一致させる必要がある（internal/app/e2e.go）。
export const remotePort = Number(process.env.E2E_REMOTE_PORT ?? 18765);
export const controlPort = Number(process.env.E2E_CONTROL_PORT ?? 18766);

// ブラウザが開くリモートルーター。偽の tailnet が報告する IP（127.0.0.1）の Host で届くので、Host 検査（NFR-S02）を通る。
export const remoteURL = `http://127.0.0.1:${remotePort}`;
// ホスト側の操作と、偽の injector に届いた入力の読み取り（e2e ビルドだけの制御エンドポイント。loopback のみ）。
export const controlURL = `http://127.0.0.1:${controlPort}`;
