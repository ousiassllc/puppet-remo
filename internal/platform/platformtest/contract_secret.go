package platformtest

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// randomToken は実行ごとに変わる偽の値を返す（固定の秘密情報をテストに書かないため）。
func randomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// RunSecretStoreContract は platform.SecretStore の契約を検査する。
// 名前は実行ごとに変わる専用の値を使い、終了時に削除するので、本物の秘密情報（SecretOpenAI など）には触れない。
func RunSecretStoreContract(t *testing.T, newStore func(t *testing.T) platform.SecretStore) {
	t.Helper()

	t.Run("Get of an unregistered name is ErrNotFound", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Get("puppet-remo/contract-" + randomToken(t)); !errors.Is(err, platform.ErrNotFound) {
			t.Errorf("Get: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("Set, overwrite, Delete", func(t *testing.T) {
		s := newStore(t)
		name := "puppet-remo/contract-" + randomToken(t)
		t.Cleanup(func() { _ = s.Delete(name) })

		first, second := randomToken(t), randomToken(t)
		if err := s.Set(name, first); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if got, err := s.Get(name); err != nil || got != first {
			t.Errorf("Get after Set: matches = %v, error = %v", got == first, err)
		}
		if err := s.Set(name, second); err != nil {
			t.Fatalf("Set (overwrite): %v", err)
		}
		if got, err := s.Get(name); err != nil || got != second {
			t.Errorf("Get after overwrite: matches = %v, error = %v", got == second, err)
		}
		if err := s.Delete(name); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Get(name); !errors.Is(err, platform.ErrNotFound) {
			t.Errorf("Get after Delete: error = %v, want ErrNotFound", err)
		}
		if err := s.Delete(name); !errors.Is(err, platform.ErrNotFound) {
			t.Errorf("second Delete: error = %v, want ErrNotFound", err)
		}
	})

	t.Run("an empty name is rejected", func(t *testing.T) {
		s := newStore(t)
		_, err := s.Get("")
		requireCode(t, err, domain.ErrInvalidArgument, "Get")
		requireCode(t, s.Set("", randomToken(t)), domain.ErrInvalidArgument, "Set")
		requireCode(t, s.Delete(""), domain.ErrInvalidArgument, "Delete")
	})
}
