// Package atoms は共有の最小の UI 要素（docs/architecture/frontend.md §4）。UI 固有の URL は持たず、props で受ける。
package atoms

import (
	"context"
	"strconv"

	"github.com/a-h/templ"
)

// Attrs は HTML 属性のパススルー（hx-* など）。
type Attrs = templ.Attributes

// Merge は属性を後勝ちで結合する。
func Merge(sets ...Attrs) Attrs {
	out := Attrs{}
	for _, s := range sets {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

type describedByKey struct{}

type describedBy struct{ id, ids string }

// WithDescribedBy は、id の入力欄に aria-describedby=ids を付けるよう、children の描画に伝える。
// FormField が、ヒント・エラー文の id を children の入力欄に結ぶ（入力欄の atom は ID が一致するときだけ受け取る）。
func WithDescribedBy(ctx context.Context, id, ids string) context.Context {
	return context.WithValue(ctx, describedByKey{}, describedBy{id: id, ids: ids})
}

// describedAttrs は attrs に、ctx で伝えられた aria-describedby を足す（id が一致するときだけ。attrs は書き換えない）。
func describedAttrs(ctx context.Context, id string, attrs Attrs) Attrs {
	d, ok := ctx.Value(describedByKey{}).(describedBy)
	if !ok || d.id != id || d.ids == "" {
		return attrs
	}
	return Merge(attrs, Attrs{"aria-describedby": d.ids})
}
