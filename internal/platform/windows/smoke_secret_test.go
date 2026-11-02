//go:build windows && smoke

package windows_test

import (
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// SecretStore の実機のスモーク（資格情報マネージャー）。契約テストは実行ごとに変わる専用の名前
// （puppet-remo/contract-<乱数>）を使って終了時に削除するので、本物の名前（puppet-remo/openai・puppet-remo/typesafe）には触れない。
func TestSmokeSecretStore(t *testing.T) {
	platformtest.RunSecretStoreContract(t, func(*testing.T) platform.SecretStore { return pwin.NewSecretStore() })
}
