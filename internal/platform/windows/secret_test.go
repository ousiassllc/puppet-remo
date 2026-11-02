//go:build !windows || !smoke

package windows

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// これらのテストは go-keyring の偽のプロバイダ（メモリ上）へ差し替えるので、本物の資格情報マネージャーには触れない。
// 差し替えはプロセス全体に及ぶため、実機のスモーク（windows && smoke）と同じバイナリには入れない（上のビルドタグ）。
// 並列実行しない。

func TestSecretStoreContract(t *testing.T) {
	platformtest.RunSecretStoreContract(t, func(t *testing.T) platform.SecretStore {
		keyring.MockInit()
		return SecretStore{}
	})
}

// 名前と値は偽物（実在のキーではない）。
const (
	fakeSecretName  = "puppet-remo/test-secret"
	fakeSecretValue = "sk-test-not-a-real-key-0123456789"
)

func TestSecretStoreEmptyValueIsRejected(t *testing.T) {
	keyring.MockInit()
	err := SecretStore{}.Set(fakeSecretName, "")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Set of an empty value: error = %v, want ErrInvalidArgument", err)
	}
	if _, err := (SecretStore{}).Get(fakeSecretName); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("a rejected Set must not register anything: Get error = %v, want ErrNotFound", err)
	}
}

// 同じ name は同じ資格情報（service = name・固定の user）に写り、別の name とは混ざらない。
func TestSecretStoreNamesAreIndependent(t *testing.T) {
	keyring.MockInit()
	s := SecretStore{}
	if err := s.Set(platform.SecretOpenAI, "openai-fake"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(platform.SecretTypeSafe, "typesafe-fake"); err != nil {
		t.Fatal(err)
	}
	if got, err := keyring.Get(platform.SecretOpenAI, secretUser); err != nil || got != "openai-fake" {
		t.Errorf("OpenAI credential: matches = %v, error = %v", got == "openai-fake", err)
	}
	if err := s.Delete(platform.SecretOpenAI); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(platform.SecretTypeSafe); err != nil || got != "typesafe-fake" {
		t.Errorf("deleting one secret must leave the other: matches = %v, error = %v", got == "typesafe-fake", err)
	}
}

// wincred の上限（2560 バイト）を超える値は ErrInvalidArgument になり、エラーに値が出ない。
func TestSecretStoreTooBigValueIsInvalidArgumentWithoutEcho(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrSetDataTooBig)
	value := fakeSecretValue + strings.Repeat("x", 3000)
	err := SecretStore{}.Set(fakeSecretName, value)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Set: error = %v, want ErrInvalidArgument", err)
	}
	if strings.Contains(err.Error(), fakeSecretValue) {
		t.Errorf("the error echoes the secret: %q", err.Error())
	}
}

// 想定外の失敗（資格情報マネージャーのエラー）は包んで返し、名前も値も文字列に入れない。
func TestSecretStoreOtherErrorsAreWrappedWithoutSecrets(t *testing.T) {
	backend := errors.New("credential manager unavailable")
	keyring.MockInitWithError(backend)
	s := SecretStore{}
	calls := map[string]error{
		"Get":    func() error { _, err := s.Get(fakeSecretName); return err }(),
		"Set":    s.Set(fakeSecretName, fakeSecretValue),
		"Delete": s.Delete(fakeSecretName),
	}
	for op, err := range calls {
		if !errors.Is(err, backend) {
			t.Errorf("%s: error = %v, want it to wrap the backend error", op, err)
			continue
		}
		if errors.Is(err, platform.ErrNotFound) || errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: a backend failure must not look like not-found / invalid-argument: %v", op, err)
		}
		for _, secret := range []string{fakeSecretName, fakeSecretValue} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error contains %q: %q", op, secret, err.Error())
			}
		}
	}
}
