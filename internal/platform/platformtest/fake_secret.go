package platformtest

import (
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.SecretStore = (*FakeSecretStore)(nil)

// FakeSecretStore は platform.SecretStore のメモリ上の偽の実装。
// 値は記録（Calls）に残さない（秘密情報がテストの出力に出ないように、Set の引数は name だけを残す）。
type FakeSecretStore struct {
	Recorder

	mu      sync.Mutex
	secrets map[string]string
}

func NewFakeSecretStore() *FakeSecretStore {
	return &FakeSecretStore{secrets: make(map[string]string)}
}

func checkSecretName(name string) error {
	if name == "" {
		return domain.ErrInvalidArgument.WithMsg("secret name is empty")
	}
	return nil
}

func (f *FakeSecretStore) Get(name string) (string, error) {
	if err := f.enterSync("Get", name); err != nil {
		return "", err
	}
	if err := checkSecretName(name); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.secrets[name]
	if !ok {
		return "", platform.ErrNotFound
	}
	return v, nil
}

func (f *FakeSecretStore) Set(name, value string) error {
	if err := f.enterSync("Set", name); err != nil {
		return err
	}
	if err := checkSecretName(name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[name] = value
	return nil
}

func (f *FakeSecretStore) Delete(name string) error {
	if err := f.enterSync("Delete", name); err != nil {
		return err
	}
	if err := checkSecretName(name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.secrets[name]; !ok {
		return platform.ErrNotFound
	}
	delete(f.secrets, name)
	return nil
}
