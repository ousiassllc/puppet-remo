package windows

import (
	"context"
	"errors"
	"testing"
)

// memRunKey は RunKey のメモリ上の偽物。
type memRunKey struct {
	values map[string]string
	err    error
}

func (m *memRunKey) Get(name string) (string, bool, error) {
	if m.err != nil {
		return "", false, m.err
	}
	v, ok := m.values[name]
	return v, ok, nil
}

func (m *memRunKey) Set(name, value string) error {
	if m.err != nil {
		return m.err
	}
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.values[name] = value
	return nil
}

func (m *memRunKey) Delete(name string) error {
	if m.err != nil {
		return m.err
	}
	delete(m.values, name)
	return nil
}

const testExe = `C:\Program Files\puppet-remo\puppet-remo.exe`

func TestCommandQuotesThePathAndAddsTheBackgroundArgument(t *testing.T) {
	got := Command(testExe)
	want := `"C:\Program Files\puppet-remo\puppet-remo.exe" --background`
	if got != want {
		t.Errorf("Command = %s, want %s", got, want)
	}
}

func TestAutostartEnableDisableRoundTrip(t *testing.T) {
	ctx := context.Background()
	key := &memRunKey{}
	a := NewAutostartWithKey(key, AutostartValueName, Command(testExe))

	if on, err := a.Enabled(ctx); err != nil || on {
		t.Fatalf("Enabled before Set = %v, %v; want false, nil", on, err)
	}
	if err := a.Set(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, err := a.Enabled(ctx); err != nil || !on {
		t.Fatalf("Enabled after Set(true) = %v, %v; want true, nil", on, err)
	}
	if got := key.values[AutostartValueName]; got != Command(testExe) {
		t.Errorf("registered value = %q, want %q", got, Command(testExe))
	}
	if err := a.Set(ctx, false); err != nil {
		t.Fatal(err)
	}
	if on, err := a.Enabled(ctx); err != nil || on {
		t.Fatalf("Enabled after Set(false) = %v, %v; want false, nil", on, err)
	}
	// 登録が無いときの無効化はエラーにしない。
	if err := a.Set(ctx, false); err != nil {
		t.Errorf("disabling an absent value: %v", err)
	}
}

func TestAutostartTreatsAnotherPathAsDisabledAndEnableOverwritesIt(t *testing.T) {
	ctx := context.Background()
	key := &memRunKey{values: map[string]string{AutostartValueName: Command(`D:\old\puppet-remo.exe`)}}
	a := NewAutostartWithKey(key, AutostartValueName, Command(testExe))

	if on, err := a.Enabled(ctx); err != nil || on {
		t.Fatalf("a registration for another path = %v, %v; want disabled", on, err)
	}
	if err := a.Set(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, _ := a.Enabled(ctx); !on {
		t.Error("enabling must overwrite the stale registration")
	}
}

func TestAutostartComparesThePathCaseInsensitively(t *testing.T) {
	key := &memRunKey{values: map[string]string{AutostartValueName: `"c:\program files\puppet-remo\PUPPET-REMO.EXE" --background`}}
	a := NewAutostartWithKey(key, AutostartValueName, Command(testExe))
	if on, err := a.Enabled(context.Background()); err != nil || !on {
		t.Errorf("Enabled = %v, %v; want true (Windows paths are case-insensitive)", on, err)
	}
}

func TestAutostartPropagatesRegistryErrorsAndCancellation(t *testing.T) {
	boom := errors.New("registry unavailable")
	a := NewAutostartWithKey(&memRunKey{err: boom}, AutostartValueName, Command(testExe))
	if _, err := a.Enabled(context.Background()); !errors.Is(err, boom) {
		t.Errorf("Enabled error = %v, want %v", err, boom)
	}
	if err := a.Set(context.Background(), true); !errors.Is(err, boom) {
		t.Errorf("Set error = %v, want %v", err, boom)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	key := &memRunKey{}
	a = NewAutostartWithKey(key, AutostartValueName, Command(testExe))
	if err := a.Set(ctx, true); !errors.Is(err, context.Canceled) {
		t.Errorf("Set with a cancelled context = %v, want context.Canceled", err)
	}
	if len(key.values) != 0 {
		t.Error("a cancelled Set must not write")
	}
}
