package molecules_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
)

// 前へ／次へだけのページ送り: 端の側は押せない button（disabled）、そうでない側は差し替えの a。両側を常に描画し、中央に N ページ目を出す。
func TestPagerDisablesTheEdgeSide(t *testing.T) {
	render := func(p molecules.PagerProps) string {
		var sb strings.Builder
		if err := molecules.Pager(p).Render(context.Background(), &sb); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	base := molecules.PagerProps{PrevLabel: "前へ", NextLabel: "次へ", Target: "#list"}

	first := base
	first.Page, first.NextURL = 1, "/x?page=2"
	out := render(first)
	for _, want := range []string{`aria-label="ページ送り"`, `<span class="font-mono">1</span> ページ目`, `aria-label="前へ" disabled data-pager-prev>`, `hx-get="/x?page=2"`, `aria-label="次へ" data-ripple`, `data-pager-next>`, `hx-target="#list"`} {
		if !strings.Contains(out, want) {
			t.Errorf("first page lacks %q:\n%s", want, out)
		}
	}

	last := base
	last.Page, last.PrevURL = 3, "/x?page=2"
	out = render(last)
	for _, want := range []string{`<span class="font-mono">3</span> ページ目`, `aria-label="前へ" data-ripple`, `data-pager-prev>`, `aria-label="次へ" disabled data-pager-next>`} {
		if !strings.Contains(out, want) {
			t.Errorf("last page lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "hx-get=") != 1 {
		t.Errorf("an edge side must not be a link:\n%s", out)
	}
}

// #179: 390 幅（sm 未満）は 3 列のグリッドで「前へ・N ページ目・次へ」を 1 行に並べ、ボタンは短い表示（‹ 前へ / 次へ ›。見た目だけ。aria-hidden）にする。
// ボタンの名前（aria-label）は呼び出し元の PrevLabel / NextLabel のまま（「新しいほう／古いほう」の意味を保つ）。sm 以上は従来どおり、両端寄せの flex で長い表示。
func TestPagerIsOneRowOnNarrowWidthsWithShortLabels(t *testing.T) {
	var sb strings.Builder
	p := molecules.PagerProps{Page: 2, PrevURL: "/x?page=1", NextURL: "/x?page=3", PrevLabel: "前へ（新しいタスク）", NextLabel: "次へ（古いタスク）", Target: "#list"}
	if err := molecules.Pager(p).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		`class="grid grid-cols-[1fr_auto_1fr] items-center gap-2 sm:flex sm:flex-wrap sm:justify-between"`,
		`aria-label="前へ（新しいタスク）"`, `aria-label="次へ（古いタスク）"`,
		`<span class="sm:hidden" aria-hidden="true">‹ 前へ</span>`, `<span class="sm:hidden" aria-hidden="true">次へ ›</span>`,
		`<span class="hidden sm:inline">前へ（新しいタスク）</span>`, `<span class="hidden sm:inline">次へ（古いタスク）</span>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pager lacks %q:\n%s", want, out)
		}
	}
	// 並びは 前へ → N ページ目 → 次へ（ページ番号が真ん中）。
	prev, page, next := strings.Index(out, "data-pager-prev"), strings.Index(out, "data-page="), strings.Index(out, "data-pager-next")
	if prev < 0 || page < prev || next < page {
		t.Errorf("order must be prev, page, next (got %d, %d, %d):\n%s", prev, page, next, out)
	}
}
