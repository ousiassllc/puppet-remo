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
  error: "pointer-events-auto flex items-start gap-3 rounded-md border border-red-300 bg-red-50 px-4 py-3 text-sm text-red-800 shadow",
  info: "pointer-events-auto flex items-start gap-3 rounded-md border border-slate-300 bg-white px-4 py-3 text-sm text-slate-800 shadow",
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

// 6. <remo-screen-viewer> が発火する remo:error（bubbles / composed。detail は { code, message }）のトースト。
document.addEventListener("remo:error", (event) => {
  const message = (event as CustomEvent<{ message?: unknown }>).detail?.message;
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
