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
    // 閉じる処理が走るなら、クリックの同期の中で data-leaving が付く。
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

  it("すべての子が取り除かれたら、空白のテキストノードも消して空にする（:empty）", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div id="modal-root"><div id="modal"><button data-clear="#modal-root">閉じる</button></div> </div>`);
    holder.querySelector<HTMLElement>("button")?.click();
    await until(() => holder.children.length === 0, "the modal is removed");
    expect(holder.childNodes.length).to.equal(0);
  });

  it("フェード中に差し込まれた新しい子は、取り除かない", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div id="modal-root"><div id="modal"></div></div>`);
    const fragment = document.createDocumentFragment();
    fragment.append(Object.assign(document.createElement("div"), { id: "modal-root" }));
    const detail = { target: holder, fragment, shouldSwap: true };
    holder.dispatchEvent(new CustomEvent("htmx:oobBeforeSwap", { bubbles: true, detail }));
    const fresh = document.createElement("div");
    fresh.id = "fresh-modal";
    holder.append(fresh);
    await until(() => holder.querySelector("#modal") === null, "the leaving modal is removed");
    expect(holder.querySelector("#fresh-modal")).to.not.equal(null);
  });
});

describe("確認モーダルを閉じたときのフォーカス", () => {
  // 確認の GET の応答が #modal-root に差し込まれた（htmx:afterSwap。requestConfig.elt が確認を開いた要素）。
  async function openModal(): Promise<{ opener: HTMLButtonElement; holder: HTMLElement; cancel: HTMLButtonElement; close: HTMLButtonElement; confirm: HTMLButtonElement }> {
    const page = await fixture<HTMLElement>(html`
      <div>
        <button id="opener">削除</button>
        <div id="modal-root">
          <div id="modal">
            <div id="panel">
              <button id="close" data-clear="#modal-root" aria-label="閉じる">×</button>
              <button id="cancel" data-clear="#modal-root">キャンセル</button>
              <button id="confirm">確定</button>
            </div>
          </div>
        </div>
      </div>
    `);
    const opener = page.querySelector<HTMLButtonElement>("#opener")!;
    const holder = page.querySelector<HTMLElement>("#modal-root")!;
    holder.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true, detail: { target: holder, requestConfig: { elt: opener } } }));
    return {
      opener,
      holder,
      cancel: page.querySelector<HTMLButtonElement>("#cancel")!,
      close: page.querySelector<HTMLButtonElement>("#close")!,
      confirm: page.querySelector<HTMLButtonElement>("#confirm")!,
    };
  }

  for (const pick of ["close", "cancel"] as const) {
    it(`${pick === "close" ? "×" : "キャンセル"}を押すと閉じて、フォーカスを開いた要素へ戻す`, async () => {
      const m = await openModal();
      m[pick].focus();
      m[pick].click();
      expect(m.holder.querySelector("#modal")?.hasAttribute("data-leaving")).to.equal(true);
      expect(document.activeElement).to.equal(m.opener);
      await until(() => m.holder.children.length === 0, "the modal is removed");
    });
  }

  it("外側（スクラム）のクリックで閉じたときも、フォーカスをモーダルの中に置いたままなら開いた要素へ戻す", async () => {
    const m = await openModal();
    m.confirm.focus();
    m.holder.querySelector<HTMLElement>("#modal")!.click();
    expect(document.activeElement).to.equal(m.opener);
  });

  it("確定の応答の OOB で閉じるときも、開いた要素が残っていれば戻す", async () => {
    const m = await openModal();
    m.confirm.focus();
    const fragment = document.createDocumentFragment();
    fragment.append(Object.assign(document.createElement("div"), { id: "modal-root" }));
    const detail = { target: m.holder, fragment, shouldSwap: true };
    m.holder.dispatchEvent(new CustomEvent("htmx:oobBeforeSwap", { bubbles: true, detail }));
    expect(document.activeElement).to.equal(m.opener);
  });

  it("フォーカスがモーダルの外の別の要素にあるなら、奪わない", async () => {
    const m = await openModal();
    const other = document.createElement("button");
    m.holder.before(other);
    other.focus();
    m.cancel.click();
    expect(document.activeElement).to.equal(other);
    other.remove();
  });

  it("開いた要素が取り除かれていたら、そこへは戻さない", async () => {
    const m = await openModal();
    m.cancel.focus();
    m.opener.remove();
    m.cancel.click();
    expect(document.activeElement).to.not.equal(m.opener);
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

  it("表示の 3 秒は、入場のアニメーションが終わってから数える", async () => {
    const root = await fixture<HTMLDivElement>(html`<div id="toast-root"><div data-toast>新しい</div></div>`);
    const toast = root.querySelector<HTMLElement>("[data-toast]");
    const entrance = toast?.animate({ opacity: [0, 1] }, { duration: 100000 });
    entrance?.pause();
    const delays: number[] = [];
    const original = window.setTimeout;
    window.setTimeout = ((handler: TimerHandler, delay?: number, ...args: unknown[]) => {
      delays.push(delay ?? 0);
      return original(handler, 1_000_000, ...args);
    }) as typeof window.setTimeout;
    try {
      root.dispatchEvent(new CustomEvent("htmx:oobAfterSwap", { bubbles: true, detail: { target: root } }));
      // 入場（一時停止中）は終わらない。1 フレーム待っても、タイマーが始まらないことを見る（tsconfig の lib が Promise.withResolvers を持たない）。
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      expect(delays, "入場の最中はタイマーを始めない").to.deep.equal([]);
      entrance?.finish();
      await until(() => delays.length > 0, "the timer starts after the entrance ends");
      expect(delays).to.deep.equal([3000]);
    } finally {
      window.setTimeout = original;
    }
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

describe("モバイルナビのドロワー", () => {
  const markup = html`
    <div>
      <div id="nav-scrim"></div>
      <nav id="sidebar-nav" aria-label="メイン">
        <p id="sidebar-nav-title">メニュー</p>
        <div id="nav-live" role="status"></div>
        <a href="#a" id="nav-first">一覧</a><a href="#b" id="nav-last">設定</a>
      </nav>
      <div id="background" data-nav-background>
        <button data-nav-toggle aria-expanded="false" aria-label="メニューを開く">開く</button>
        <main><button id="before">本文の操作</button></main>
      </div>
      <div id="toast-root" role="status"></div>
    </div>
  `;
  const isOpen = (): boolean => document.body.hasAttribute("data-nav-open");

  afterEach(() => {
    document.body.removeAttribute("data-nav-open");
  });

  it("ハンバーガーで開閉し、aria-expanded とラベルを追従させ、開いたら先頭のリンクにフォーカスする", async () => {
    const root = await fixture<HTMLElement>(markup);
    const toggle = root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!;
    toggle.click();
    expect(isOpen()).to.equal(true);
    expect(toggle.getAttribute("aria-expanded")).to.equal("true");
    expect(toggle.getAttribute("aria-label")).to.equal("メニューを閉じる");
    expect(document.activeElement?.id).to.equal("nav-first");
    toggle.click();
    expect(isOpen()).to.equal(false);
    expect(toggle.getAttribute("aria-expanded")).to.equal("false");
    expect(toggle.getAttribute("aria-label")).to.equal("メニューを開く");
  });

  it("スクラム・ナビのリンクのクリックで閉じる", async () => {
    const root = await fixture<HTMLElement>(markup);
    root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!.click();
    root.querySelector<HTMLElement>("#nav-scrim")!.click();
    expect(isOpen()).to.equal(false);
    root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!.click();
    root.querySelector<HTMLElement>("#nav-first")!.click();
    expect(isOpen()).to.equal(false);
  });

  it("Esc で閉じて、フォーカスをハンバーガーに戻す。閉じているときの Esc は何もしない", async () => {
    const root = await fixture<HTMLElement>(markup);
    const toggle = root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!;
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(document.activeElement).to.not.equal(toggle);
    toggle.click();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(isOpen()).to.equal(false);
    expect(document.activeElement).to.equal(toggle);
  });

  it("開いている間は背面（data-nav-background）を inert にし、閉じたら外す", async () => {
    const root = await fixture<HTMLElement>(markup);
    const background = root.querySelector<HTMLElement>("#background")!;
    const toggle = root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!;
    expect(background.inert).to.equal(false);
    toggle.click();
    expect(background.inert).to.equal(true);
    expect(root.querySelector<HTMLElement>("#sidebar-nav")!.inert).to.equal(false);
    toggle.click();
    expect(background.inert).to.equal(false);
  });

  it("Tab はドロワーの中を循環し、背面へ抜けない（末尾で Tab → 先頭、先頭で Shift+Tab → 末尾、外にあれば中へ戻す）", async () => {
    const root = await fixture<HTMLElement>(markup);
    root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!.click();
    const first = root.querySelector<HTMLElement>("#nav-first")!;
    const last = root.querySelector<HTMLElement>("#nav-last")!;
    const press = (shiftKey: boolean): KeyboardEvent => {
      const event = new KeyboardEvent("keydown", { key: "Tab", shiftKey, cancelable: true });
      document.dispatchEvent(event);
      return event;
    };
    last.focus();
    expect(press(false).defaultPrevented).to.equal(true);
    expect(document.activeElement).to.equal(first);
    expect(press(true).defaultPrevented).to.equal(true);
    expect(document.activeElement).to.equal(last);
    // 中間のリンクでは、ブラウザの既定の移動に任せる。
    first.focus();
    expect(press(false).defaultPrevented).to.equal(false);
    // フォーカスが（inert でない）ドロワーの外へ出ていたら、中へ戻す。
    root.querySelector<HTMLElement>("#sidebar-nav")!.blur();
    (document.activeElement as HTMLElement | null)?.blur();
    expect(press(false).defaultPrevented).to.equal(true);
    expect(document.activeElement).to.equal(first);
  });

  it("開いている間だけ role=dialog・aria-modal・見出しへの aria-labelledby を付け、閉じたら外す（常設のナビ・閉じたナビをダイアログとして見せない）", async () => {
    const root = await fixture<HTMLElement>(markup);
    const nav = root.querySelector<HTMLElement>("#sidebar-nav")!;
    const toggle = root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!;
    const dialogAttrs = (): (string | null)[] => ["role", "aria-modal", "aria-labelledby"].map((name) => nav.getAttribute(name));
    expect(dialogAttrs()).to.deep.equal([null, null, null]);
    toggle.click();
    expect(dialogAttrs()).to.deep.equal(["dialog", "true", "sidebar-nav-title"]);
    // aria-labelledby の指す先が、見出しの文言「メニュー」になっている。
    expect(root.querySelector("#sidebar-nav-title")?.textContent).to.equal("メニュー");
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(dialogAttrs()).to.deep.equal([null, null, null]);
  });

  it("閉じると、開く前にフォーカスのあった要素へ戻す（ハンバーガー以外でも）。閉じているときの Tab は何もしない", async () => {
    const root = await fixture<HTMLElement>(markup);
    const before = root.querySelector<HTMLButtonElement>("#before")!;
    const toggle = root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!;
    const tab = new KeyboardEvent("keydown", { key: "Tab", cancelable: true });
    document.dispatchEvent(tab);
    expect(tab.defaultPrevented).to.equal(false);
    before.focus();
    toggle.click();
    expect(document.activeElement?.id).to.equal("nav-first");
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(document.activeElement).to.equal(before);
  });
});

describe("ドロワーが開いている間のトースト", () => {
  const markup = html`
    <div>
      <nav id="sidebar-nav" aria-label="メイン">
        <p id="sidebar-nav-title">メニュー</p>
        <div id="nav-live" role="status"></div>
        <a href="#a" id="nav-first">一覧</a><a href="#b" id="nav-last">設定</a>
      </nav>
      <div id="background" data-nav-background>
        <button data-nav-toggle aria-expanded="false" aria-label="メニューを開く">開く</button>
      </div>
      <div id="toast-root" role="status"></div>
    </div>
  `;
  const isOpen = (): boolean => document.body.hasAttribute("data-nav-open");
  const showToast = (root: HTMLElement, message: string): HTMLElement => {
    const toastRoot = root.querySelector<HTMLElement>("#toast-root")!;
    const toast = Object.assign(document.createElement("div"), { textContent: message });
    toast.setAttribute("data-toast", "");
    toastRoot.append(toast);
    toastRoot.dispatchEvent(new CustomEvent("htmx:oobAfterSwap", { bubbles: true, detail: { target: toastRoot } }));
    return toast;
  };

  afterEach(() => {
    document.body.removeAttribute("data-nav-open");
  });

  it("トーストの内容は、ドロワーの中の #nav-live で読み上げられ、トーストの置き場は二重に読ませない。閉じたら元に戻る", async () => {
    const root = await fixture<HTMLElement>(markup);
    const live = root.querySelector<HTMLElement>("#nav-live")!;
    const toastRoot = root.querySelector<HTMLElement>("#toast-root")!;
    root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!.click();
    expect(toastRoot.getAttribute("aria-hidden")).to.equal("true");
    showToast(root, "キルスイッチを発動しました。");
    expect(live.textContent).to.equal("キルスイッチを発動しました。");
    showToast(root, "設定を保存しました。");
    expect(live.textContent, "新しいトーストが前の読み上げを置き換える").to.equal("設定を保存しました。");
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(toastRoot.hasAttribute("aria-hidden")).to.equal(false);
    expect(live.textContent).to.equal("");
  });

  it("ドロワーが閉じているときのトーストは、#toast-root の読み上げだけに任せる", async () => {
    const root = await fixture<HTMLElement>(markup);
    showToast(root, "保存しました。");
    expect(root.querySelector("#nav-live")?.textContent).to.equal("");
    expect(root.querySelector("#toast-root")?.hasAttribute("aria-hidden")).to.equal(false);
  });

  it("トーストが出ても、背面は inert のまま・トーストは Tab の対象にならず、フォーカスはドロワーの中にとどまる", async () => {
    const root = await fixture<HTMLElement>(markup);
    root.querySelector<HTMLButtonElement>("[data-nav-toggle]")!.click();
    expect(isOpen()).to.equal(true);
    const toast = showToast(root, "保存しました。");
    const toastRoot = root.querySelector<HTMLElement>("#toast-root")!;
    // トーストは背面（inert）の外にあり（読み上げが届く）、操作できる部品を持たない（Tab・クリックの対象にならない）。
    expect(toastRoot.closest("[inert]")).to.equal(null);
    expect(toast.querySelector("a[href], button, input, select, textarea, [tabindex]")).to.equal(null);
    expect(root.querySelector<HTMLElement>("#background")!.inert).to.equal(true);
    // Tab はドロワーの中を循環する（トーストが差し込まれても外へ抜けない）。
    const last = root.querySelector<HTMLElement>("#nav-last")!;
    last.focus();
    const tab = new KeyboardEvent("keydown", { key: "Tab", cancelable: true });
    document.dispatchEvent(tab);
    expect(tab.defaultPrevented).to.equal(true);
    expect(document.activeElement?.id).to.equal("nav-first");
    expect(toastRoot.contains(document.activeElement)).to.equal(false);
  });
});

describe("秘密の入力欄（API キー）", () => {
  const savedMarkup = html`
    <div data-secret>
      <div data-secret-saved><span>登録済み</span><button type="button" data-secret-change>変更</button></div>
      <form>
        <div data-secret-edit hidden>
          <input id="key-input" name="api_key" type="password" />
          <button type="button" data-secret-toggle aria-label="入力を表示する" aria-pressed="false" aria-controls="key-input"></button>
        </div>
        <button type="submit" data-secret-edit hidden>置き換える</button>
      </form>
    </div>
  `;

  it("「変更」で、バッジの行を隠して空の入力欄と登録ボタンを出し、入力欄にフォーカスする", async () => {
    const root = await fixture<HTMLElement>(savedMarkup);
    const input = root.querySelector<HTMLInputElement>("#key-input")!;
    root.querySelector<HTMLElement>("[data-secret-change]")!.click();
    expect(root.querySelector<HTMLElement>("[data-secret-saved]")!.hidden).to.equal(true);
    for (const edit of root.querySelectorAll<HTMLElement>("[data-secret-edit]")) {
      expect(edit.hidden).to.equal(false);
    }
    expect(input.value).to.equal("");
    expect(document.activeElement).to.equal(input);
  });

  it("表示切り替えは type=password ⇄ text と aria-pressed を合わせ、入力した値は変えない", async () => {
    const root = await fixture<HTMLElement>(savedMarkup);
    const input = root.querySelector<HTMLInputElement>("#key-input")!;
    const toggle = root.querySelector<HTMLButtonElement>("[data-secret-toggle]")!;
    input.value = "sk-typed";
    toggle.click();
    expect(input.type).to.equal("text");
    expect(toggle.getAttribute("aria-pressed")).to.equal("true");
    expect(input.value).to.equal("sk-typed");
    toggle.click();
    expect(input.type).to.equal("password");
    expect(toggle.getAttribute("aria-pressed")).to.equal("false");
  });
});

describe("前へ／次へだけのページ送り", () => {
  // 差し替え後の一覧（#list）。端の側は disabled の button、そうでない側は a。
  const pager = (prevDisabled: boolean, nextDisabled: boolean) => html`
    <div id="list">
      <nav data-pager>
        ${prevDisabled ? html`<button type="button" disabled data-pager-prev>前へ</button>` : html`<a href="/p" data-pager-prev>前へ</a>`}
        <span>2 ページ目</span>
        ${nextDisabled ? html`<button type="button" disabled data-pager-next>次へ</button>` : html`<a href="/n" data-pager-next>次へ</a>`}
      </nav>
    </div>
  `;
  // 押したリンク（差し替えを起こしたリクエストの要素。htmx:afterSwap の detail.requestConfig.elt）と、その差し替え。
  const pressed = (side: "prev" | "next" | null): HTMLElement => {
    const a = document.createElement("a");
    if (side) {
      a.setAttribute(`data-pager-${side}`, "");
    }
    return a;
  };
  const swapped = (list: HTMLElement, elt: Element): void => {
    list.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true, detail: { target: list, requestConfig: { elt } } }));
  };

  it("押した側がまだ使えるなら、その側のボタンにフォーカスを戻す", async () => {
    const list = await fixture<HTMLElement>(pager(false, false));
    swapped(list, pressed("next"));
    expect(document.activeElement).to.equal(list.querySelector("[data-pager-next]"));
  });

  it("次へが端になって disabled なら、反対側（前へ）へフォーカスを移す", async () => {
    const list = await fixture<HTMLElement>(pager(false, true));
    swapped(list, pressed("next"));
    expect(document.activeElement).to.equal(list.querySelector("[data-pager-prev]"));
  });

  it("前へが端になって disabled なら、反対側（次へ）へフォーカスを移す", async () => {
    const list = await fixture<HTMLElement>(pager(true, false));
    swapped(list, pressed("prev"));
    expect(document.activeElement).to.equal(list.querySelector("[data-pager-next]"));
  });

  it("ページ送りが起こしていない差し替えでは、フォーカスを動かさない", async () => {
    const list = await fixture<HTMLElement>(pager(false, false));
    swapped(list, pressed(null));
    expect(document.activeElement).to.not.equal(list.querySelector("[data-pager-next]"));
    expect(document.activeElement).to.not.equal(list.querySelector("[data-pager-prev]"));
  });

  it("別のリクエスト（ポーリング）の差し替えが先に起きても、そのあとのページ送りの差し替えでフォーカスを移す（押した側を状態に持たない）", async () => {
    const list = await fixture<HTMLElement>(pager(false, true));
    swapped(list, pressed(null));
    expect(document.activeElement).to.not.equal(list.querySelector("[data-pager-prev]"));
    swapped(list, pressed("next"));
    expect(document.activeElement).to.equal(list.querySelector("[data-pager-prev]"));
  });
});

describe("アラートを見える位置へ寄せる", () => {
  const alertMarkup = html`
    <div id="alert-root"></div>
    <template id="alert-template"><div data-alert-tone="danger"><p data-alert-message></p></div></template>
  `;

  // scrollIntoView の呼び出しを記録する（実際のスクロールはしない）。
  function recordScroll(root: Element): ScrollIntoViewOptions[] {
    const calls: ScrollIntoViewOptions[] = [];
    root.scrollIntoView = (arg?: boolean | ScrollIntoViewOptions) => {
      calls.push(typeof arg === "object" ? arg : {});
    };
    return calls;
  }

  it("アクションの失敗の差し込み（afterSwap）と OOB（oobAfterSwap）のあと、block: nearest で寄せる", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div>${alertMarkup}</div>`);
    const root = holder.querySelector("#alert-root")!;
    const calls = recordScroll(root);
    root.append(document.createElement("div"));
    root.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true, detail: { target: root } }));
    root.dispatchEvent(new CustomEvent("htmx:oobAfterSwap", { bubbles: true, detail: { target: root } }));
    expect(calls).to.deep.equal([{ block: "nearest" }, { block: "nearest" }]);
  });

  it("アラートが空のとき、#alert-root 以外の差し込みでは寄せない", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div>${alertMarkup}<div id="elsewhere"><i></i></div></div>`);
    const root = holder.querySelector("#alert-root")!;
    const other = holder.querySelector("#elsewhere")!;
    const rootCalls = recordScroll(root);
    const otherCalls = recordScroll(other);
    root.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true, detail: { target: root } }));
    other.dispatchEvent(new CustomEvent("htmx:afterSwap", { bubbles: true, detail: { target: other } }));
    expect(rootCalls.length + otherCalls.length).to.equal(0);
  });

  it("remo:error のアラートも寄せ、同じ文言のときは置き換えない", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div>${alertMarkup}</div>`);
    const root = holder.querySelector("#alert-root")!;
    const calls = recordScroll(root);
    const detail = { code: "input_blocked", message: "blocked" };
    document.dispatchEvent(new CustomEvent("remo:error", { detail }));
    const first = root.firstElementChild;
    expect(first).to.not.equal(null);
    document.dispatchEvent(new CustomEvent("remo:error", { detail }));
    expect(root.firstElementChild).to.equal(first);
    expect(calls.length).to.equal(1); // 新規作成のときだけ。重複の抑止では寄せない
  });

  it("退出中（data-leaving）のアラートと同じ文言は、置き換える（表示中のものだけを重複と見なす）", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div>${alertMarkup}</div>`);
    const root = holder.querySelector("#alert-root")!;
    const detail = { code: "input_blocked", message: "blocked" };
    document.dispatchEvent(new CustomEvent("remo:error", { detail }));
    const first = root.firstElementChild!;
    first.setAttribute("data-leaving", "");
    document.dispatchEvent(new CustomEvent("remo:error", { detail }));
    expect(root.children.length).to.equal(1);
    expect(root.firstElementChild).to.not.equal(first);
    expect(root.firstElementChild?.hasAttribute("data-leaving")).to.equal(false);
    expect(root.querySelector("[data-alert-message]")?.textContent).to.equal(first.querySelector("[data-alert-message]")?.textContent);
  });
});

describe("スライダーの充填と数値", () => {
  const sliderMarkup = html`<div>
    <input id="s" type="range" min="1" max="30" step="1" value="1" data-range-output="s-out" />
    <output id="s-out">1</output>
  </div>`;

  it("input で、数値の表示と充填の割合（--_v）が値に追従する", async () => {
    const holder = await fixture<HTMLDivElement>(sliderMarkup);
    const range = holder.querySelector<HTMLInputElement>("#s")!;
    range.value = "30";
    range.dispatchEvent(new Event("input", { bubbles: true }));
    expect(holder.querySelector("#s-out")?.textContent).to.equal("30");
    expect(range.style.getPropertyValue("--_v")).to.equal("100%");
    range.value = "1";
    range.dispatchEvent(new Event("input", { bubbles: true }));
    expect(range.style.getPropertyValue("--_v")).to.equal("0%");
  });

  it("HTMX が差し込んだ要素（htmx:load）の充填を、値に合わせる", async () => {
    const holder = await fixture<HTMLDivElement>(html`<div><input type="range" min="0" max="100" value="25" /></div>`);
    holder.dispatchEvent(new CustomEvent("htmx:load", { bubbles: true }));
    expect(holder.querySelector("input")?.style.getPropertyValue("--_v")).to.equal("25%");
  });
});
