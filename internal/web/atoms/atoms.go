// Package atoms は共有の最小の UI 要素（docs/architecture/frontend.md §4）。UI 固有の URL は持たず、props で受ける。
package atoms

import "github.com/a-h/templ"

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
