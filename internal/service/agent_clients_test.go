package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// 本物のクライアント（openai.New・typesafe.New）が呼び出しのたびに読むキーの窓口は、資格情報マネージャー（SecretStore）の
// 決まった名前を読み、保持しない（登録・削除がすぐに反映される）。未登録は platform.ErrNotFound。
func TestSecretReaderReadsTheStoreEveryTime(t *testing.T) {
	store := platformtest.NewFakeSecretStore()
	read := secretReader(store, platform.SecretOpenAI)
	if _, err := read(context.Background()); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("unregistered: %v", err)
	}
	if err := store.Set(platform.SecretOpenAI, "sk-1"); err != nil {
		t.Fatal(err)
	}
	if got, err := read(context.Background()); err != nil || got != "sk-1" {
		t.Fatalf("registered: %q, %v", got, err)
	}
	if err := store.Set(platform.SecretOpenAI, "sk-2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := read(context.Background()); got != "sk-2" {
		t.Errorf("after rotation: %q", got)
	}
	if _, err := secretReader(store, platform.SecretTypeSafe)(context.Background()); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("the TypeSafe key is a different secret: %v", err)
	}
}

// 組み立ては通信しない（キーが無くても作れる）。
func TestNewAgentClientsDoNotNeedKeysToBuild(t *testing.T) {
	store := platformtest.NewFakeSecretStore()
	if NewAgentModel(store) == nil || NewAgentChooser(store) == nil {
		t.Fatal("the clients must be built without calling out")
	}
}
