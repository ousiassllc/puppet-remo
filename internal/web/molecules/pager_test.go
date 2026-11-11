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
	for _, want := range []string{`aria-label="ページ送り"`, "1 ページ目", `disabled data-pager-prev>前へ</button>`, `hx-get="/x?page=2"`, `data-pager-next>次へ</a>`, `hx-target="#list"`} {
		if !strings.Contains(out, want) {
			t.Errorf("first page lacks %q:\n%s", want, out)
		}
	}

	last := base
	last.Page, last.PrevURL = 3, "/x?page=2"
	out = render(last)
	for _, want := range []string{"3 ページ目", `data-pager-prev>前へ</a>`, `disabled data-pager-next>次へ</button>`} {
		if !strings.Contains(out, want) {
			t.Errorf("last page lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "hx-get=") != 1 {
		t.Errorf("an edge side must not be a link:\n%s", out)
	}
}
