// ホスト GUI の最小限のクライアントコード（docs/architecture/frontend.md §5.6・§7）。
// 状態とロジックはサーバーが持つ。ここにあるのは、サーバーに頼れない次のものだけ:
//   1. ネットワーク障害・想定外の 5xx の汎用エラー（インラインアラート。サーバーの応答が無いので、ページに置いた雛形から作る）
//   2. トークンのコピー
//   3. トースト（サーバーが OOB で追加する完了の短い通知）の自動消去（3 秒）と、新しいものが前を置き換える動作
//   4. 確認モーダルのキャンセル・外側（スクラム）クリック・トークン表示を閉じる（data-clear）。出現・退出のフェード（退出の取り除き）とボタンの押下のリップル
//   5. ビューアのツールバーのスライダーを動かしている間、横の数値（<output>）を追従させる（data-range-output）
//   6. <remo-screen-viewer> の remo:error（入力の拒否など）をインラインアラートにする
//   7. モバイルナビ（md 未満の左のドロワー）の開閉（ハンバーガー・スクラム・リンク・Esc）
//   8. アラートの差し込み（afterSwap・oobAfterSwap・新規作成）のあと、縦に長い画面でも見える位置へ寄せる（revealAlerts）
//   9. クライアントで出すエラーのアラートは、同じ文言がすでに表示中（退出中でない）なら置き換えない（ちらつき・再読み上げの防止）
//  10. ウィンドウの一覧（#window-picker）の自動更新が、フォーカス・ポインターの操作を奪わないようにする（picker-guard.ts）
//  11. 秘密の入力欄（API キー）の「変更」（登録済みのバッジを空の入力欄に入れ替える）と、入力中の値の表示切り替え（data-secret-change・data-secret-toggle）
// インラインのスクリプト・イベント属性は使わず、イベントはここで付ける。

import { installPickerRefreshGuard } from "./picker-guard";

const TOAST_ROOT = "toast-root";
const TOAST_LIFETIME_MS = 3000;
const ALERT_ROOT = "alert-root";
const ALERT_TEMPLATE = "alert-template";

// 退出のモーション（app.css の `.motion-*-in[data-leaving]`）。data-leaving を付けると CSS のフェードアウトが始まり、終わったら after を呼ぶ。
// アニメーションが無い（reduced-motion で 0 など）ときは、すぐ after を呼ぶ。
function leave(el: Element, after: () => void): void {
  el.setAttribute("data-leaving", "");
  const animations = el.getAnimations();
  void Promise.allSettled(animations.map((a) => a.finished)).then(after);
}

// root の子をフェードアウトさせ、終わったものから取り除く。フェード中に差し込まれた新しい子（data-leaving の付いていないもの）は残す。
function clearWithFade(root: Element): void {
  for (const child of [...root.children]) {
    leave(child, () => {
      child.remove();
      // 空白のテキストノードが残ると :empty が外れるので、子要素が無くなったら中身ごと空にする。
      if (root.children.length === 0) {
        root.replaceChildren();
      }
    });
  }
}

// 縦に長い画面の下のほうで失敗しても見えるように、差し込んだアラートを表示領域に入れる（すでに見えていれば動かない）。
// behavior は既定（auto）のまま滑らせない（prefers-reduced-motion を尊重する）。
function revealAlerts(root: Element): void {
  if (root.firstElementChild) {
    root.scrollIntoView({ block: "nearest" });
  }
}

// エラーのインラインアラートを #alert-root に出す（前のアラートは置き換える）。見た目は、サーバーが出すアラート（atoms.Alert）と
// 同じ雛形（#alert-template）の複製で、本文だけを textContent で差し込む。
function showErrorAlert(message: string): void {
  const root = document.getElementById(ALERT_ROOT);
  const template = document.getElementById(ALERT_TEMPLATE);
  if (!root || !(template instanceof HTMLTemplateElement)) {
    return;
  }
  // 同じ文言のアラートがすでに表示中（退出中の data-leaving の下にあるものは除く）なら置き換えない（ちらつきと、スクリーンリーダーの再読み上げを防ぐ）。
  const shown = [...root.querySelectorAll("[data-alert-message]")].find((el) => !el.closest("[data-leaving]"));
  if (shown?.textContent === message) {
    return;
  }
  const alert = template.content.firstElementChild?.cloneNode(true);
  if (!(alert instanceof HTMLElement)) {
    return;
  }
  alert.setAttribute("role", "alert");
  const text = alert.querySelector("[data-alert-message]");
  if (text) {
    text.textContent = message;
  }
  root.replaceChildren(alert);
  revealAlerts(root);
}

// 7. ウィンドウの一覧の自動更新の見送り（フォーカス・ポインターの操作中）。
installPickerRefreshGuard();

// 1. サーバーが HTML で返さないエラー（接続できない、想定外の 5xx）だけをクライアントで扱う。
document.addEventListener("htmx:sendError", () => {
  showErrorAlert("サーバーに接続できませんでした。");
});
document.addEventListener("htmx:responseError", (event) => {
  const xhr = (event as CustomEvent<{ xhr?: XMLHttpRequest }>).detail?.xhr;
  // サーバーがエラーのフラグメントを返したとき（HX-Retarget 付き）は、それが表示される。
  if (xhr && xhr.status >= 500 && !xhr.getResponseHeader("HX-Retarget")) {
    showErrorAlert("サーバーでエラーが発生しました。");
  }
});

// 7. モバイルナビのドロワー（P0）。開閉の状態は body の data-nav-open で持ち、見た目は app.css の .nav-drawer・.nav-scrim が決める
// （md 以上では CSS が無視する）。ナビは OOB で差し替わるので、状態はナビの外（body）に置く。開いたら先頭のリンクにフォーカスを移し、
// Esc で閉じてハンバーガーに戻す。md 以上へ広がったら閉じる（狭くしたときに開いたまま現れない）。
const NAV_OPEN_ATTR = "data-nav-open";
const desktopWidth = window.matchMedia("(min-width: 768px)");

function setNavOpen(open: boolean): void {
  document.body.toggleAttribute(NAV_OPEN_ATTR, open);
  const toggle = document.querySelector<HTMLElement>("[data-nav-toggle]");
  toggle?.setAttribute("aria-expanded", String(open));
  toggle?.setAttribute("aria-label", open ? "メニューを閉じる" : "メニューを開く");
  if (open) {
    document.querySelector<HTMLElement>("#sidebar-nav a")?.focus();
  }
}

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && document.body.hasAttribute(NAV_OPEN_ATTR)) {
    setNavOpen(false);
    document.querySelector<HTMLElement>("[data-nav-toggle]")?.focus();
  }
});

desktopWidth.addEventListener("change", (event) => {
  if (event.matches) {
    setNavOpen(false);
  }
});

// 2. トークンのコピー。クリップボードに書けなければ、全選択して Ctrl+C を案内する（フォールバックの文言は常に描画されている）。
document.addEventListener("click", (event) => {
  const target = event.target;
  if (!(target instanceof Element)) {
    return;
  }

  // 7. モバイルナビ（md 未満の左のドロワー）: ハンバーガーで開閉し、スクラム・リンクのクリックで閉じる。
  if (target.closest("[data-nav-toggle]")) {
    setNavOpen(!document.body.hasAttribute(NAV_OPEN_ATTR));
    return;
  }
  if (target.id === "nav-scrim" || target.closest("#sidebar-nav a")) {
    setNavOpen(false);
    return;
  }

  // 11. 秘密の入力欄。
  const secretToggle = target.closest<HTMLElement>("[data-secret-toggle]");
  if (secretToggle) {
    toggleSecretVisibility(secretToggle);
    return;
  }
  if (target.closest("[data-secret-change]")) {
    editSecret(target.closest("[data-secret]"));
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
    const root = document.querySelector(clear.dataset.clear ?? "");
    if (root) {
      clearWithFade(root);
    }
    return;
  }

  // 確認モーダルの外側（スクラム）クリックで閉じる。パネルの中のクリックでは閉じない。
  if (target.id === "modal") {
    const root = target.closest("#modal-root");
    if (root) {
      clearWithFade(root);
    }
    return;
  }
});

// ページ遷移（hx-boost の #main の差し替え）では、前のページのエラーのアラートを消す。
document.addEventListener("htmx:beforeSwap", (event) => {
  if ((event as CustomEvent<{ target?: Element }>).detail?.target?.id === "main") {
    document.getElementById(ALERT_ROOT)?.replaceChildren();
  }
});

// 確認の確定の応答は OOB で #modal-root を空にする（organisms.ModalClear）。そのまま差し替えると退出のフェードが無いので、
// 差し替えを止めて、フェードアウトのあとに空にする。
document.addEventListener("htmx:oobBeforeSwap", (event) => {
  const detail = (event as CustomEvent<{ target: Element; fragment: ParentNode; shouldSwap: boolean }>).detail;
  if (detail.target.id === "modal-root" && detail.fragment.firstElementChild?.childElementCount === 0) {
    detail.shouldSwap = false;
    clearWithFade(detail.target);
  }
});

// 4. ボタン・クローズ（data-ripple）の押下のリップル: クリック点から円が広がって消える（app.css の .ripple。reduced-motion では出さない）。
const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
document.addEventListener("pointerdown", (event) => {
  if (reducedMotion.matches || event.button !== 0 || !(event.target instanceof Element)) {
    return;
  }
  const host = event.target.closest<HTMLElement>("[data-ripple]");
  if (!host || (host instanceof HTMLButtonElement && host.disabled)) {
    return;
  }
  const rect = host.getBoundingClientRect();
  const ripple = document.createElement("span");
  ripple.className = "ripple";
  ripple.setAttribute("aria-hidden", "true");
  // style 属性（setAttribute）は CSP の style-src で止まるので、CSSOM のプロパティの設定だけを使う。
  ripple.style.setProperty("--ripple-x", `${event.clientX - rect.left}px`);
  ripple.style.setProperty("--ripple-y", `${event.clientY - rect.top}px`);
  ripple.style.setProperty("--ripple-size", `${Math.max(rect.width, rect.height) * 2}px`);
  ripple.addEventListener("animationend", () => ripple.remove(), { once: true });
  host.append(ripple);
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

// 11. 入力中の値の表示切り替え。type=password ⇄ text と aria-pressed を合わせる（ラベルは固定。アイコンは aria-pressed を見て CSS が切り替える）。
function toggleSecretVisibility(button: HTMLElement): void {
  const input = document.getElementById(button.getAttribute("aria-controls") ?? "");
  if (!(input instanceof HTMLInputElement)) {
    return;
  }
  const shown = input.type === "password";
  input.type = shown ? "text" : "password";
  button.setAttribute("aria-pressed", String(shown));
}

// 11. 登録済みのバッジと「変更」を隠し、空の入力欄と登録ボタンを出して、入力欄にフォーカスを移す。
// 見せ分けはサーバーが描画した hidden 属性で、ここでは入れ替えるだけ（値は持たない・戻さない。NFR-S14）。
function editSecret(root: Element | null): void {
  if (!root) {
    return;
  }
  for (const saved of root.querySelectorAll<HTMLElement>("[data-secret-saved]")) {
    saved.hidden = true;
  }
  for (const edit of root.querySelectorAll<HTMLElement>("[data-secret-edit]")) {
    edit.hidden = false;
  }
  root.querySelector<HTMLElement>("input")?.focus();
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

// 6. <remo-screen-viewer> が発火する remo:error（bubbles / composed。detail は { code, message }）のインラインアラート。
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

// 停止にならない拒否（スコープ付きのリースの、その操作だけの拒否。#157）の理由ごとの文言。code より優先する。
const wsReasonMessages: Readonly<Record<string, string>> = {
  clipboard_out_of_scope: "ウィンドウを選んだ操作中は、クリップボードの読み取りは使えません（書き込みだけ使えます）。",
  key_out_of_scope: "このキーは、ウィンドウを選んだ操作中は送れません。",
  target_halted: "対象の操作は停止中です。「再開」を押すか、別のウィンドウを選んでください。",
};

document.addEventListener("remo:error", (event) => {
  const detail = (event as CustomEvent<{ code?: unknown; message?: unknown; reason?: unknown }>).detail;
  const code = detail?.code;
  const reason = detail?.reason;
  const reasonMessage = typeof reason === "string" && Object.hasOwn(wsReasonMessages, reason) ? wsReasonMessages[reason] : undefined;
  const mapped = reasonMessage ?? (typeof code === "string" && Object.hasOwn(wsErrorMessages, code) ? wsErrorMessages[code] : undefined);
  const message = mapped ?? detail?.message;
  showErrorAlert(typeof message === "string" && message !== "" ? message : "操作を実行できませんでした。");
});

// 5. スライダー（画質・FPS）。送信は離したとき（change）にサーバーへ送られ、サーバーが再描画する。
// 横の数値（<output>）を追従させ、充填の幅（--_v。app.css の Blink 用のトラック）を値の割合にする。style 属性は CSP で使えないので CSSOM で設定する。
function syncRangeFill(range: HTMLInputElement): void {
  const min = range.min === "" ? 0 : Number(range.min);
  const max = range.max === "" ? 100 : Number(range.max);
  const ratio = max > min ? (Number(range.value) - min) / (max - min) : 0;
  range.style.setProperty("--_v", `${Math.min(Math.max(ratio, 0), 1) * 100}%`);
}

function syncRangeFills(root: ParentNode): void {
  for (const range of root.querySelectorAll<HTMLInputElement>('input[type="range"]')) {
    syncRangeFill(range);
  }
}

document.addEventListener("input", (event) => {
  const target = event.target;
  if (!(target instanceof HTMLInputElement) || target.type !== "range") {
    return;
  }
  syncRangeFill(target);
  const output = document.getElementById(target.dataset.rangeOutput ?? "");
  if (output) {
    output.textContent = target.value;
  }
});

// 読み込み時と、HTMX が差し込んだ新しい要素（ツールバーの再描画）の充填を合わせる。
syncRangeFills(document);
document.addEventListener("htmx:load", (event) => {
  if (event.target instanceof Element) {
    syncRangeFills(event.target);
  }
});

// 3. サーバーが OOB（beforeend:#toast-root）で追加したトースト。同時に表示するのは 1 つで、新しいものが前を置き換える。
// 表示は 3 秒で、フェードアウトして消える。
function settleToasts(root: Element): void {
  const toasts = [...root.querySelectorAll<HTMLElement>("[data-toast]")];
  const latest = toasts.pop();
  for (const previous of toasts) {
    previous.remove();
  }
  if (!latest || latest.hasAttribute("data-toast-timer")) {
    return;
  }
  latest.setAttribute("data-toast-timer", "");
  window.setTimeout(() => {
    if (latest.isConnected) {
      leave(latest, () => latest.remove());
    }
  }, TOAST_LIFETIME_MS);
}

document.addEventListener("htmx:oobAfterSwap", (event) => {
  const target = (event as CustomEvent<{ target?: Element }>).detail?.target;
  if (target?.id === TOAST_ROOT) {
    settleToasts(target);
  } else if (target?.id === ALERT_ROOT) {
    revealAlerts(target);
  }
});

// アクションの失敗（HX-Retarget: #alert-root の応答）の差し込み。アラートが見える位置へ寄せる（OOB は上の oobAfterSwap）。
document.addEventListener("htmx:afterSwap", (event) => {
  const target = (event as CustomEvent<{ target?: Element }>).detail?.target;
  if (target?.id === ALERT_ROOT) {
    revealAlerts(target);
  }
});
