package platform

import "errors"

// ErrNotFound は SecretStore に名前が登録されていないことを表す。errors.Is で判定する。
var ErrNotFound = errors.New("platform: secret not found")

// SecretStore は資格情報マネージャー（go-keyring）。name は下記の定数。M1 では使わない。
// 空の name は ErrInvalidArgument。
type SecretStore interface {
	Get(name string) (string, error) // 未登録は ErrNotFound
	Set(name, value string) error
	Delete(name string) error // 未登録は ErrNotFound
}

const (
	SecretOpenAI   = "puppet-remo/openai"
	SecretTypeSafe = "puppet-remo/typesafe" // 高速パス。値は登録後に表示しない（FR-G22）
)
