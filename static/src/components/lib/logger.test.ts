// lib/logger.ts のテスト。テストの実行では __DEBUG__ を false にする（本番ビルドと同じ。web-test-runner.config.mjs）ので、
// debug / info は出さず、warn / error だけを scope 付きで出す。
import { expect } from "@open-wc/testing";
import { createLogger } from "./logger";

describe("createLogger", () => {
  const levels = ["debug", "info", "warn", "error"] as const;
  const original = { debug: console.debug, info: console.info, warn: console.warn, error: console.error };
  let calls: { level: string; args: unknown[] }[];

  beforeEach(() => {
    calls = [];
    for (const level of levels) {
      console[level] = (...args: unknown[]) => {
        calls.push({ level, args });
      };
    }
  });

  afterEach(() => {
    for (const level of levels) {
      console[level] = original[level];
    }
  });

  it("本番の設定では warn / error だけを、scope を前置して出す", () => {
    const log = createLogger("ws");
    log.debug("d");
    log.info("i");
    log.warn("w", 1);
    log.error("e", { a: 2 });
    expect(calls).to.deep.equal([
      { level: "warn", args: ["[ws] w", 1] },
      { level: "error", args: ["[ws] e", { a: 2 }] },
    ]);
  });
});
