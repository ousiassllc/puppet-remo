package windows

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// secretUser は go-keyring に渡す固定のユーザー名。go-keyring は Windows の資格情報のターゲット名を
// `service + ":" + user` で作る（wincred の GenericCredential）ので、name を service に渡すと、資格情報マネージャー上の
// ターゲット名は `<name>:puppet-remo`（例: `puppet-remo/openai:puppet-remo`）になる。ターゲット名を name と完全に同じにはできない
// （go-keyring に user を省く経路がなく、空にしても末尾に `:` が付く）。固定値なので、名前から資格情報は一意に決まる。
const secretUser = "puppet-remo"

var _ platform.SecretStore = (*SecretStore)(nil)

// SecretStore は platform.SecretStore の実装（go-keyring。Windows では資格情報マネージャー＝DPAPI でユーザーアカウントに束縛。NFR-S14）。
// 状態を持たず、どの goroutine からも呼べる。
//
// 秘密の値は、返り値（Get）以外のどこにも出さない: エラーは go-keyring・wincred の返す値を含まないエラー（Errno・番兵）だけを
// 包み、値・名前を文字列に埋めない。ログは出さない。
// NewSecretStore は Windows のビルドだけにある（secret_windows.go）。
type SecretStore struct{}

// Get は name の秘密を返す。未登録は platform.ErrNotFound、空の name は domain.ErrInvalidArgument。
func (SecretStore) Get(name string) (string, error) {
	if err := checkSecretName(name); err != nil {
		return "", err
	}
	v, err := keyring.Get(name, secretUser)
	if err != nil {
		return "", secretError("read", err)
	}
	return v, nil
}

// Set は name の秘密を登録する（登録済みなら上書き）。空の name・空の値・上限（wincred は 2560 バイト）超えは domain.ErrInvalidArgument。
func (SecretStore) Set(name, value string) error {
	if err := checkSecretName(name); err != nil {
		return err
	}
	if value == "" {
		return domain.ErrInvalidArgument.WithMsg("secret value is empty")
	}
	return secretError("write", keyring.Set(name, secretUser, value))
}

// Delete は name の秘密を削除する。未登録は platform.ErrNotFound。
func (SecretStore) Delete(name string) error {
	if err := checkSecretName(name); err != nil {
		return err
	}
	return secretError("delete", keyring.Delete(name, secretUser))
}

func checkSecretName(name string) error {
	if name == "" {
		return domain.ErrInvalidArgument.WithMsg("secret name is empty")
	}
	return nil
}

// secretError は go-keyring のエラーを platform のエラーへ写す（nil は nil）。
// 大きすぎる値は呼び出し側の引数の誤りとして ErrInvalidArgument にする（メッセージに値の長さも含めない）。
func secretError(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNotFound):
		return platform.ErrNotFound
	case errors.Is(err, keyring.ErrSetDataTooBig):
		return domain.ErrInvalidArgument.WithMsg("secret is too large")
	default:
		return fmt.Errorf("windows: secret store %s: %w", op, err)
	}
}
