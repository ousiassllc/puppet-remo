package organisms

import (
	"encoding/json"

	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// flagAttrs は有効・無効の切り替えボタンの hx-* 属性。hx-vals は JSON のデータ（js: 接頭辞は使わない。CSP: §7.1）。
func flagAttrs(f view.ClientFlag, cardID string) atoms.Attrs {
	vals, err := json.Marshal(map[string]string{f.Field: f.NextValue})
	if err != nil { // map[string]string は常に変換できる
		panic(err)
	}
	return atoms.Attrs{
		"hx-patch":        f.ToggleURL,
		"hx-vals":         string(vals),
		"hx-target":       "#" + cardID,
		"hx-swap":         "outerHTML",
		"hx-disabled-elt": "this",
	}
}
