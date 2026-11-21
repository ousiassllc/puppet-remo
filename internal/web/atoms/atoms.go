// Package atoms は共有の最小の UI 要素（docs/architecture/frontend.md §4）。UI 固有の URL は持たず、props で受ける。
package atoms

import (
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

// ScrollRegion はスクロールする領域（overflow-auto / overflow-x-auto の要素）に付ける属性（B6）。
// 領域の中にフォーカスできる要素が無くても Tab で領域自体にフォーカスでき、矢印・PageUp/PageDown・Space・Home/End でスクロールできるよう
// tabindex="0" にし、role="region" と名前（label）を付ける。フォーカスリングは app.css の `:focus-visible`。
func ScrollRegion(label string) Attrs {
	return Attrs{"tabindex": "0", "role": "region", "aria-label": label}
}

func itoa(n int) string { return strconv.Itoa(n) }
