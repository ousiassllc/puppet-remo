//go:build production

package static

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// distFS は go:embed した dist を返す。
func distFS() (fs.FS, bool, error) {
	sub, err := fs.Sub(embedded, "dist")
	return sub, true, err
}
