// ホスト GUI の最小限のクライアントコード（docs/architecture/frontend.md §5.6・§7）。
// 状態とロジックはサーバーが持つ。ここにあるのは、サーバーに頼れない次のものだけ:
//   1. ネットワーク障害・想定外の 5xx の汎用トースト
//   2. トークンのコピー
//   3. トースト（サーバーが OOB で追加する）の閉じる操作と自動消去
//   4. 確認モーダルのキャンセル・トークン表示を閉じる（data-clear）
//   5. ビューアのツールバーのスライダーを動かしている間、横の数値（<output>）を追従させる（data-range-output）
//   6. <remo-screen-viewer> の remo:error（入力の拒否など）をトーストにする
// インラインのスクリプト・イベント属性は使わず、イベントはここで付ける。

const TOAST_ROOT = "toast-root";
const TOAST_LIFETIME_MS = 8000;

const toastClasses = {
  error: "pointer-events-auto flex items-start gap-3 rounded-md border border-danger/40 bg-danger/10 px-4 py-3 text-sm text-danger",
  info: "pointer-events-auto flex items-start gap-3 rounded-md border border-border bg-surface px-4 py-3 text-sm text-ink",
};

function showErrorToast(message: string): void {
  const root = document.getElementById(TOAST_ROOT);
  if (!root) {
    return;
  }
  const toast = document.createElement("div");
  toast.className = toastClasses.error;
  toast.setAttribute("role", "alert");
  toast.setAttribute("data-toast", "");
  toast.setAttribute("data-toast-level", "error");
  const text = document.createElement("span");
  text.textContent = message;
  toast.append(text);
  root.append(toast);
}

// 1. サーバーが HTML で返さないエラー（接続できない、想定外の 5xx）だけをクライアントで扱う。
document.addEventListener("htmx:sendError", () => {
  showErrorToast("サーバーに接続できませんでした。");
});
document.addEventListener("htmx:responseError", (event) => {
  const xhr = (event as CustomEvent<{ xhr?: XMLHttpRequest }>).detail?.xhr;
  // サーバーがエラーのフラグメントを返したとき（HX-Retarget 付き）は、それが表示される。
  if (xhr && xhr.status >= 500 && !xhr.getResponseHeader("HX-Retarget")) {
    showErrorToast("サーバーでエラーが発生しました。");
  }
});

// 2. トークンのコピー。クリップボードに書けなければ、全選択して Ctrl+C を案内する（フォールバックの文言は常に描画されている）。
document.addEventListener("click", (event) => {
  const target = event.target;
  if (!(target instanceof Element)) {
    return;
  }

  const copyButton = target.closest<HTMLElement>("[data-copy-target]");
  if (copyButton) {
    const input = document.getElementById(copyButton.dataset.copyTarget ?? "");
    if (input instanceof HTMLInputElement) {
      void copyValue(input, copyButton);
    }
    return;
  }

  // data-clear="<selector>": 対象を空にする（確認モーダルのキャンセル、発行したトークンの表示を閉じる）。
  // 確認モーダルの確定の応答は、サーバーが OOB で #modal-root を空にする。
  const clear = target.closest<HTMLElement>("[data-clear]");
  if (clear) {
    document.querySelector(clear.dataset.clear ?? "")?.replaceChildren();
    return;
  }

  // 3. トーストを閉じる。
  const dismiss = target.closest("[data-toast-dismiss]");
  if (dismiss) {
    dismiss.closest("[data-toast]")?.remove();
  }
});

async function copyValue(input: HTMLInputElement, button: HTMLElement): Promise<void> {
  const status = document.getElementById(button.dataset.copyStatus ?? "");
  try {
    await navigator.clipboard.writeText(input.value);
    if (status) {
      status.textContent = status.dataset.copied ?? "";
    }
  } catch {
    input.focus();
    input.select();
    if (status) {
      status.textContent = status.dataset.fallback ?? "";
    }
  }
}

// 7. 承認ダイアログ（専用の Wails ウィンドウ。/approvals/{id}）の許可・拒否の応答は HX-Trigger: closeWindow を返す。
// htmx がこのイベントを（要素から document まで bubbles で）発火するので、ウィンドウを閉じる。Wails のランタイム（/wails/runtime.js。
// 同一オリジンの外部スクリプトで、CSP の script-src 'self' に収まる。Wails の AssetServer が配信する）の Window.Close を使う。
// ウィンドウは承認の終了（許可・拒否・失効）を購読した app 側も閉じる（internal/app）ので、ここが失敗しても（ブラウザでの開発など
// ランタイムが無いとき）残るのは「許可しました。」の表示だけで、承認の結果には影響しない。
const wailsRuntimeURL = "/wails/runtime.js";

document.addEventListener("closeWindow", () => {
  void (async () => {
    try {
      const runtime: { Window: { Close(): Promise<void> } } = await import(wailsRuntimeURL);
      await runtime.Window.Close();
    } catch {
      // ランタイムが無い・閉じられない。ウィンドウは app が閉じる。
    }
  })();
});

// 6. <remo-screen-viewer> が発火する remo:error（bubbles / composed。detail は { code, message }）のトースト。
// WebSocket の error の message はドメインエラーの文（英語）で、契約はそのまま。利用者向けには code から日本語にする。
// 文言は HTMX のアクションの失敗（internal/web/handler/remote/errors.go の failureMessages）と揃える。表に無い code はサーバーの message を出す。
const wsErrorMessages: Readonly<Record<string, string>> = {
  lease_required: "操作権がありません。先に「操作権を取得」を押してください。",
  lease_held_by_other: "ほかのセッションが操作権を保持しています。",
  lease_revoked: "操作権が強制的に外されました。改めて取得してください。",
  approval_denied: "ホストが操作権の取得を拒否しました。",
  approval_timeout: "ホストから時間内に応答がなく、操作権を取得できませんでした。",
  invalid_argument: "入力内容を確認してください。",
  kill_switch_active: "キルスイッチが発動中です。",
  desktop_unavailable: "ホストの画面を操作できません（ロック画面など）。",
  input_blocked: "ホストの前面のウィンドウには入力できません。",
  internal: "内部エラーが発生しました。",
};

document.addEventListener("remo:error", (event) => {
  const detail = (event as CustomEvent<{ code?: unknown; message?: unknown }>).detail;
  const code = detail?.code;
  const mapped = typeof code === "string" && Object.hasOwn(wsErrorMessages, code) ? wsErrorMessages[code] : undefined;
  const message = mapped ?? detail?.message;
  showErrorToast(typeof message === "string" && message !== "" ? message : "操作を実行できませんでした。");
});

// 5. スライダー（画質・FPS）の値の表示。送信は離したとき（change）にサーバーへ送られ、サーバーが再描画する。
document.addEventListener("input", (event) => {
  const target = event.target;
  if (!(target instanceof HTMLInputElement) || target.type !== "range") {
    return;
  }
  const output = document.getElementById(target.dataset.rangeOutput ?? "");
  if (output) {
    output.textContent = target.value;
  }
});

// 3. サーバーが追加したトーストを一定時間後に消す。
const toastRoot = document.getElementById(TOAST_ROOT);
if (toastRoot) {
  new MutationObserver((records) => {
    for (const record of records) {
      for (const node of record.addedNodes) {
        if (node instanceof HTMLElement && node.hasAttribute("data-toast")) {
          window.setTimeout(() => node.remove(), TOAST_LIFETIME_MS);
        }
      }
    }
  }).observe(toastRoot, { childList: true });
}
