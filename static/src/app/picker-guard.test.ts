import { expect, fixture, html } from "@open-wc/testing";
import { PICKER_ID, installPickerRefreshGuard, pickerBusy } from "./picker-guard";

// ウィンドウの一覧の自動更新のガード（#157）。htmx の本体は使わず、htmx:beforeRequest を直接発火して確かめる。
describe("picker-guard", () => {
  installPickerRefreshGuard();

  const picker = () =>
    html`<section id=${PICKER_ID}>
      <ul>
        <li data-window-id="1"><button type="button">選ぶ</button></li>
      </ul>
      <button type="button" id="refresh">一覧を更新</button>
    </section>`;

  // htmx が発行する htmx:beforeRequest（キャンセル可能）。triggeringEvent があれば、ポーリング以外（リースの変化・クリック）の再取得。
  function beforeRequest(el: Element, triggeringEvent?: Event): boolean {
    const event = new CustomEvent("htmx:beforeRequest", { bubbles: true, cancelable: true, detail: { requestConfig: { triggeringEvent } } });
    el.dispatchEvent(event);
    return !event.defaultPrevented; // true = 要求は出る
  }

  it("何も操作していなければ、ポーリングの更新は通る", async () => {
    const el = await fixture(picker());
    expect(pickerBusy(el)).to.equal(false);
    expect(beforeRequest(el)).to.equal(true);
  });

  it("一覧の中の要素にフォーカスがあるあいだは、ポーリングの更新を見送る", async () => {
    const el = await fixture(picker());
    el.querySelector<HTMLButtonElement>("li button")?.focus();
    expect(pickerBusy(el)).to.equal(true);
    expect(beforeRequest(el)).to.equal(false);
    // フォーカスが外れれば通る。
    (document.activeElement as HTMLElement).blur();
    expect(beforeRequest(el)).to.equal(true);
  });

  it("リースの変化・手動の更新（triggeringEvent あり）は、フォーカス中でも止めない", async () => {
    const el = await fixture(picker());
    el.querySelector<HTMLButtonElement>("li button")?.focus();
    expect(beforeRequest(el, new CustomEvent("remo:lease-changed"))).to.equal(true);
    expect(beforeRequest(el, new MouseEvent("click"))).to.equal(true);
  });

  it("一覧以外の要素の要求には関与しない", async () => {
    const el = await fixture(html`<div id="other"><button type="button">x</button></div>`);
    el.querySelector("button")?.focus();
    expect(beforeRequest(el)).to.equal(true);
  });
});
