//go:build windows

package windows

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// registryRunKey は現在のユーザーの Run キー。管理者権限は要らない。
type registryRunKey struct{}

func openRunKey(access uint32) (registry.Key, error) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, access)
	if err != nil {
		return 0, fmt.Errorf("open HKCU\\%s: %w", runKeyPath, err)
	}
	return k, nil
}

func (registryRunKey) Get(name string) (string, bool, error) {
	k, err := openRunKey(registry.QUERY_VALUE)
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read autostart value: %w", err)
	}
	return v, true, nil
}

func (registryRunKey) Set(name, value string) error {
	k, err := openRunKey(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue(name, value); err != nil {
		return fmt.Errorf("write autostart value: %w", err)
	}
	return nil
}

func (registryRunKey) Delete(name string) error {
	k, err := openRunKey(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("delete autostart value: %w", err)
	}
	return nil
}

// NewAutostart は、実行中のアプリを HKCU の Run キーへ登録する Autostart を返す。
func NewAutostart() (*Autostart, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate the executable: %w", err)
	}
	return NewAutostartWithKey(registryRunKey{}, AutostartValueName, Command(exe)), nil
}
