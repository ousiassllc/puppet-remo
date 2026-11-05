// app.ts（ホスト GUI の最小限のクライアントコード）のテスト。docs/architecture/frontend.md §5.6・§9。
// app.ts は読み込み時にリスナーを付ける。このテストは app.css を読み込まないので、退出のフェード（data-leaving のあとの
// アニメーションの終了待ち）はアニメーションが無く、次のマイクロタスクで終わる。終了は条件待ち（until）で確かめる。
import { expect, fixture, html } from "@open-wc/testing";
import { until } from "../components/testing/frames";
import "./app";

const modalMarkup = html`<div id="modal-root"><div id="modal"><div id="panel"><button>確定</button></div></div></div>`;

describe("確認モーダルを閉じる", () => {
  it("外側（スクラム）のクリックで閉じる", async () => {
    const holder = await fixture<HTMLDivElement>(modalMarkup);
    holder.querySelector<HTMLElement>("#modal")?.click();
    await until(() => holder.children.length === 0, "the modal is removed");
  });

  it("パネルの中のクリックでは閉じない", async () => {
    const holder = await fixture<HTMLDivElement>(modalMarkup);
    holder.querySelector<HTMLElement>("#panel")?.click();
    holder.querySelector<HTMLElement>("button")?.click();
    // 閉じる処理が走るなら、数フレームのうちに data-leaving が付くか、中身が空になる。
    for (let i = 0; i < 5; i++) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    expect(holder.querySelector("#modal")).to.not.equal(null);
    expect(holder.querySelector("#modal")?.hasAttribute("data-leaving")).to.equal(false);
  });

  it("data-clear のボタンで、対象の中身にフェードアウトを始めさせ、終わったら空にする", async () => {
    const holder = await fixture<HTMLDivElement>(
      html`<div id="modal-root"><div id="modal"><button data-clear="#modal-root">キャンセル</button></div></div>`,
    );
    holder.querySelector<HTMLElement>("button")?.click();
    expect(holder.querySelector("#modal")?.hasAttribute("data-leaving")).to.equal(true);
    await until(() => holder.children.length === 0, "the modal is removed");
  });

  it("確定の応答の OOB（空の #modal-root）は、差し替えを止めてフェードアウトで閉じる", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div id="modal-root"><div id="modal"></div></div>`);
    const fragment = document.createDocumentFragment();
    fragment.append(Object.assign(document.createElement("div"), { id: "modal-root" }));
    const detail = { target: holder, fragment, shouldSwap: true };
    holder.dispatchEvent(new CustomEvent("htmx:oobBeforeSwap", { bubbles: true, detail }));
    expect(detail.shouldSwap).to.equal(false);
    expect(holder.querySelector("#modal")?.hasAttribute("data-leaving")).to.equal(true);
    await until(() => holder.children.length === 0, "the modal is removed");
  });
});

describe("トースト", () => {
  it("OOB で追加された新しいトーストは、前のトーストを置き換える（同時に 1 つ）", async () => {
    const root = await fixture<HTMLDivElement>(
      html`<div id="toast-root"><div data-toast>古い</div><div data-toast>新しい</div></div>`,
    );
    root.dispatchEvent(new CustomEvent("htmx:oobAfterSwap", { bubbles: true, detail: { target: root } }));
    const left = [...root.querySelectorAll("[data-toast]")];
    expect(left.length).to.equal(1);
    expect(left[0].textContent).to.equal("新しい");
    expect(left[0].hasAttribute("data-toast-timer")).to.equal(true);
  });

  it("#toast-root 以外への OOB には触らない", async () => {
    const other = await fixture<HTMLDivElement>(html`<div id="elsewhere"><div data-toast>a</div><div data-toast>b</div></div>`);
    other.dispatchEvent(new CustomEvent("htmx:oobAfterSwap", { bubbles: true, detail: { target: other } }));
    expect(other.querySelectorAll("[data-toast]").length).to.equal(2);
  });
});

describe("エラーのアラート", () => {
  const alertMarkup = html`
    <div id="alert-root"><div data-stale>前のアラート</div></div>
    <template id="alert-template"><div data-alert-tone="danger"><p data-alert-message></p></div></template>
  `;

  it("サーバーに接続できないとき、雛形から作ったアラートで、前のアラートを置き換える", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div>${alertMarkup}</div>`);
    document.dispatchEvent(new CustomEvent("htmx:sendError"));
    const root = holder.querySelector("#alert-root");
    expect(root?.children.length).to.equal(1);
    expect(root?.querySelector("[data-stale]")).to.equal(null);
    const alert = root?.firstElementChild;
    expect(alert?.getAttribute("role")).to.equal("alert");
    expect(alert?.querySelector("[data-alert-message]")?.textContent).to.equal("サーバーに接続できませんでした。");
  });

  it("ページ遷移（#main の差し替え）で、前のページのアラートを消す", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div>${alertMarkup}<main id="main"></main></div>`);
    const main = holder.querySelector("#main");
    main?.dispatchEvent(new CustomEvent("htmx:beforeSwap", { bubbles: true, detail: { target: main } }));
    expect(holder.querySelector("#alert-root")?.children.length).to.equal(0);
  });
});

describe("ボタンの押下のリップル", () => {
  it("data-ripple のボタンの押下で、クリック点を中心にした円を足し、アニメーションの終了で取り除く", async () => {
    const button = await fixture<HTMLButtonElement>(html`<button data-ripple>押す</button>`);
    button.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, clientX: 5, clientY: 6 }));
    const ripple = button.querySelector<HTMLElement>(".ripple");
    expect(ripple).to.not.equal(null);
    expect(ripple?.style.getPropertyValue("--ripple-size")).to.not.equal("");
    ripple?.dispatchEvent(new Event("animationend"));
    expect(button.querySelector(".ripple")).to.equal(null);
  });

  it("無効のボタン・data-ripple の無い要素には出さない", async () => {
    const disabled = await fixture<HTMLButtonElement>(html`<button data-ripple disabled>無効</button>`);
    disabled.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0 }));
    expect(disabled.querySelector(".ripple")).to.equal(null);
    const plain = await fixture<HTMLButtonElement>(html`<button>ふつう</button>`);
    plain.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0 }));
    expect(plain.querySelector(".ripple")).to.equal(null);
  });
});
