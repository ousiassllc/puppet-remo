package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func testDenied(id string, kind domain.DeniedKind, value string) domain.DeniedApp {
	at := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	return domain.DeniedApp{ID: id, Kind: kind, Value: value, CreatedAt: at, UpdatedAt: at}
}

func TestLaunchDenylistCreateGetListDelete(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchDenylist()

	byName := testDenied("01A", domain.DeniedName, "cmd.exe")
	byPath := testDenied("01B", domain.DeniedPath, `C:\Windows\System32\powershell.exe`)
	for _, d := range []domain.DeniedApp{byPath, byName} {
		if err := s.Create(ctx, d, domain.MaxDeniedApps); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(ctx, "01A")
	if err != nil || !reflect.DeepEqual(got, byName) {
		t.Fatalf("Get = %+v, %v, want %+v", got, err, byName)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "01A" || list[1].ID != "01B" { // kind の昇順: name、path
		t.Errorf("List = %+v, %v, want [name, path]", list, err)
	}
	if err := s.Delete(ctx, "01A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "01A"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "01A"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete missing = %v, want ErrNotFound", err)
	}
}

// 大文字小文字と区切りの違いは同じ登録として重複になる。Update も他の行と重複させない。
func TestLaunchDenylistRejectsDuplicates(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchDenylist()

	if err := s.Create(ctx, testDenied("01A", domain.DeniedName, "cmd.exe"), domain.MaxDeniedApps); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, testDenied("01B", domain.DeniedName, "CMD.EXE"), domain.MaxDeniedApps); !errors.Is(err, ErrConflict) {
		t.Errorf("Create(case variant) = %v, want ErrConflict", err)
	}
	if err := s.Create(ctx, testDenied("01C", domain.DeniedPath, `C:\Tools\a.exe`), domain.MaxDeniedApps); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, testDenied("01D", domain.DeniedPath, `c:/tools//A.EXE`), domain.MaxDeniedApps); !errors.Is(err, ErrConflict) {
		t.Errorf("Create(path spelled differently) = %v, want ErrConflict", err)
	}
	// 名前と同じ綴りでも、kind が違えば別の登録。
	if err := s.Create(ctx, testDenied("01E", domain.DeniedPath, `C:\cmd.exe`), domain.MaxDeniedApps); err != nil {
		t.Errorf("Create(different kind) = %v", err)
	}
	upd := testDenied("01C", domain.DeniedName, "CMD.exe")
	if err := s.Update(ctx, upd); !errors.Is(err, ErrConflict) {
		t.Errorf("Update(to a duplicate) = %v, want ErrConflict", err)
	}
	upd.Value = "notepad.exe"
	if err := s.Update(ctx, upd); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ := s.Get(ctx, "01C"); got.Kind != domain.DeniedName || got.Value != "notepad.exe" {
		t.Errorf("after Update = %+v", got)
	}
	if err := s.Update(ctx, testDenied("missing", domain.DeniedName, "x.exe")); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing = %v, want ErrNotFound", err)
	}
}

func TestLaunchDenylistLimit(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchDenylist()
	for _, id := range []string{"01A", "01B"} {
		if err := s.Create(ctx, testDenied(id, domain.DeniedName, id+".exe"), 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Create(ctx, testDenied("01C", domain.DeniedName, "01C.exe"), 2); !errors.Is(err, ErrLimitReached) {
		t.Errorf("Create over the limit = %v, want ErrLimitReached", err)
	}
}

// 方式の設定が無い既存のインストールは allowlist のまま（settings に launch.mode の行を作らない）。
func TestLaunchModeSettingAbsentByDefault(t *testing.T) {
	db, _ := openTestDB(t)
	if _, ok, err := db.Settings().Get(context.Background(), "launch.mode"); err != nil || ok {
		t.Errorf("launch.mode row exists after migration: ok=%v err=%v", ok, err)
	}
}
