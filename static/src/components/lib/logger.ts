// レベル付きのコンソールロガー（docs/architecture/frontend.md §6.1）。
// 本番ビルドはエラーと警告だけを出す（開発ビルドは debug から）。
// 秘密情報（トークン・クリップボードの本文・画面の内容・入力した文字）はログに渡さない。

export type LogLevel = "debug" | "info" | "warn" | "error";

const order: Record<LogLevel, number> = { debug: 0, info: 1, warn: 2, error: 3 };

const threshold: LogLevel = __DEBUG__ ? "debug" : "warn";

export interface Logger {
  debug(message: string, ...details: unknown[]): void;
  info(message: string, ...details: unknown[]): void;
  warn(message: string, ...details: unknown[]): void;
  error(message: string, ...details: unknown[]): void;
}

// createLogger は、scope（例: "ws"）を前置するロガーを返す。
export function createLogger(scope: string): Logger {
  const emit = (level: LogLevel) => (message: string, ...details: unknown[]) => {
    if (order[level] < order[threshold]) {
      return;
    }
    console[level](`[${scope}] ${message}`, ...details);
  };
  return { debug: emit("debug"), info: emit("info"), warn: emit("warn"), error: emit("error") };
}
