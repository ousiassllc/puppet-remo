// E2E の共通部品: テスト専用サーバーの制御エンドポイントのクライアントと、CSP 違反の監視（frontend.md §7.1・§9）。
import { expect, test as base } from "@playwright/test";
import { controlURL } from "./env";

async function request<T>(method: "GET" | "POST", path: string, expectedStatus = 200): Promise<T> {
  const res = await fetch(`${controlURL}${path}`, { method });
  const body = await res.text();
  if (res.status !== expectedStatus) {
    throw new Error(`${method} ${path}: HTTP ${res.status} ${body}`);
  }
  return JSON.parse(body) as T;
}

export interface PendingRequest {
  id: string;
  node_id: string;
  display_name: string;
}

export interface InputCall {
  method: string;
  // 座標は { x, y }、ボタン・操作・文字列はそのまま。
  args: unknown[];
}

// 制御エンドポイントが { x, y } として返す座標の引数を取り出す。
export function asPoint(arg: unknown): { x: number; y: number } {
  if (typeof arg === "object" && arg !== null && "x" in arg && "y" in arg && typeof arg.x === "number" && typeof arg.y === "number") {
    return { x: arg.x, y: arg.y };
  }
  throw new Error(`not a point: ${JSON.stringify(arg)}`);
}

// ホスト側の操作と読み取り。どれもテスト専用サーバー（ビルドタグ e2e）の制御エンドポイントで、サービスの本物の経路（HostAdmin・
// LeaseManager・キルスイッチ）を呼ぶ。DB の行を直接書かない。
export const host = {
  reset: () => request("POST", "/reset"),
  identify: (as: "registered" | "unregistered") => request("POST", `/identity?as=${as}`),
  pendingRequests: () => request<PendingRequest[]>("GET", "/requests"),
  allow: () => request<{ client_id: string }>("POST", "/requests/allow"),
  revokeLease: () => request("POST", "/lease/revoke"),
  triggerKillSwitch: () => request("POST", "/killswitch"),
  releaseKillSwitch: () => request("POST", "/killswitch/release"),
  inputs: () => request<InputCall[]>("GET", "/inputs"),
};

export interface CspViolation {
  url: string;
  directive: string;
  blocked: string;
}

// CSP の監視は、ブラウザが出す 2 つの信号の両方を失敗として扱う（§7.1）:
//  - securitypolicyviolation イベント（ページごとに addInitScript で window に付け、公開した関数で Node 側へ報告する。
//    ナビゲーションで window が作り直されても、init script が毎回付け直す）
//  - コンソールのエラーのうち、Content Security Policy に触れるもの
// 自動のフィクスチャなので、すべてのテストの、すべてのページに効く。テストの終了時に 1 件でもあれば失敗する。
export const test = base.extend<{ cspViolations: CspViolation[] }>({
  cspViolations: [
    async ({ context, page }, use) => {
      const violations: CspViolation[] = [];
      await context.exposeFunction("__reportCspViolation", (v: CspViolation) => {
        violations.push(v);
      });
      await context.addInitScript(() => {
        window.addEventListener(
          "securitypolicyviolation",
          (e) => {
            const report: unknown = Reflect.get(window, "__reportCspViolation");
            if (typeof report === "function") {
              report({ url: location.href, directive: e.violatedDirective, blocked: e.blockedURI });
            }
          },
          true,
        );
      });
      page.on("console", (m) => {
        if (m.type() === "error" && /content security policy/i.test(m.text())) {
          violations.push({ url: page.url(), directive: "console", blocked: m.text() });
        }
      });
      await use(violations);
      expect(violations, "CSP violations (securitypolicyviolation / console errors)").toEqual([]);
    },
    { auto: true },
  ],
});

export { expect };
