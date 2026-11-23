// ウィンドウの一覧（#window-picker。#157）の自動更新（htmx の every 3s）が、操作を奪わないための小さなガード。
// 一覧の中の要素にフォーカスがある・ポインターが行の上にあるあいだは、自動更新（ポーリング）を見送る
// （差し替えで、押そうとしたボタンやフォーカスが消えない）。手動の更新ボタン・リースの変化（remo:lease-changed）による再取得は止めない
// （選んだ直後の一覧の差し替えを遅らせない）。ポーリングは htmx が triggeringEvent なしで発行するので、それで見分ける。

export const PICKER_ID = "window-picker";

// pickerBusy は、一覧の中にフォーカスがある、またはポインターが行の上にあるか。
export function pickerBusy(picker: Element, doc: Document = document): boolean {
  const active = doc.activeElement;
  if (active !== null && active !== doc.body && picker.contains(active)) {
    return true;
  }
  return picker.querySelector("[data-window-id]:hover") !== null;
}

interface BeforeRequestDetail {
  requestConfig?: { triggeringEvent?: unknown };
}

// installPickerRefreshGuard は htmx:beforeRequest で、一覧のポーリングを busy のあいだ取り消す。
export function installPickerRefreshGuard(doc: Document = document): void {
  doc.addEventListener("htmx:beforeRequest", (event) => {
    const el = event.target;
    if (!(el instanceof Element) || el.id !== PICKER_ID) {
      return;
    }
    const detail = (event as CustomEvent<BeforeRequestDetail>).detail;
    if (detail?.requestConfig?.triggeringEvent !== undefined) {
      return; // リースの変化・手動の再取得は止めない
    }
    if (pickerBusy(el, doc)) {
      event.preventDefault();
    }
  });
}
