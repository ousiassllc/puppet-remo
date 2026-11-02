package platform

import "errors"

// ErrNotFound は対象が無いことを表す（SecretStore に名前が登録されていない、AppLauncher.Inspect のファイル・
// ディレクトリが無い）。errors.Is で判定する。メッセージには名前・パスを含めない。
var ErrNotFound = errors.New("platform: not found")

// SecretStore は資格情報マネージャー（go-keyring）。name は下記の定数。
// 空の name・空の value は ErrInvalidArgument。
type SecretStore interface {
	Get(name string) (string, error) // 未登録は ErrNotFound
	Set(name, value string) error
	Delete(name string) error // 未登録は ErrNotFound
}

const (
	SecretOpenAI   = "puppet-remo/openai"
	SecretTypeSafe = "puppet-remo/typesafe" // 高速パス。値は登録後に表示しない（FR-G22）
)
