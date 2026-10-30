package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func testApp(id, label string) domain.LaunchableApp {
	return domain.LaunchableApp{
		ID: id, Label: label, ExePath: `C:\Windows\` + id + `.exe`,
		Args: []string{"/a", `--name=x y`}, WorkingDir: `C:\Work`,
		CreatedAt: time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC),
	}
}

func appLabels(t *testing.T, s LaunchableApps) []string {
	t.Helper()
	apps, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, len(apps))
	for i, a := range apps {
		labels[i] = a.Label
	}
	return labels
}

func TestLaunchableAppsCreateGet(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	want := testApp("01A", "メモ帳")
	if err := s.Create(ctx, want, domain.MaxLaunchableApps); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "01A")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get = %+v, want %+v", got, want)
	}

	if _, err := s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}
}

// 引数なし・作業ディレクトリなしは、空の配列・空文字で往復する（nil は JSON の null にならない）。
func TestLaunchableAppsEmptyArgsAndWorkingDir(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	for id, args := range map[string][]string{"a-nil": nil, "a-empty": {}} {
		a := testApp(id, id)
		a.Args, a.WorkingDir = args, ""
		if err := s.Create(ctx, a, domain.MaxLaunchableApps); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		got, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Args == nil || len(got.Args) != 0 || got.WorkingDir != "" {
			t.Errorf("%s: Args=%#v WorkingDir=%q, want empty non-nil slice and empty string", id, got.Args, got.WorkingDir)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM launchable_apps WHERE args_json = '[]'`); n != 2 {
		t.Errorf("rows stored with args_json '[]' = %d, want 2", n)
	}
}

// 引数は 1 要素ずつそのまま往復する（引用符・空白・改行・日本語・JSON の特殊文字を含む）。
func TestLaunchableAppsArgsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	a := testApp("a1", "x")
	a.Args = []string{`"quoted"`, "with space", "line\nbreak", "日本語", `\back\slash`, `{"json":[1]}`, "'", " "}
	if err := s.Create(ctx, a, domain.MaxLaunchableApps); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Args, a.Args) {
		t.Errorf("Args = %q, want %q", got.Args, a.Args)
	}
}

func TestLaunchableAppsListOrder(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	if apps, err := s.List(ctx); err != nil || len(apps) != 0 {
		t.Fatalf("empty List = %v, %v", apps, err)
	}
	// 登録順とは無関係に、label の昇順（ASCII の大文字小文字は無視）。
	for i, label := range []string{"banana", "Cherry", "apple", "Apricot", "あ"} {
		if err := s.Create(ctx, testApp(fmt.Sprintf("id%d", i), label), domain.MaxLaunchableApps); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"apple", "Apricot", "banana", "Cherry", "あ"}
	if got := appLabels(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("List labels = %q, want %q", got, want)
	}
}

func TestLaunchableAppsLabelUniqueness(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	if err := s.Create(ctx, testApp("a1", "Notepad"), domain.MaxLaunchableApps); err != nil {
		t.Fatal(err)
	}

	// ASCII の大文字小文字違いは同じ label。
	for i, label := range []string{"notepad", "NOTEPAD", "nOtEpAd"} {
		err := s.Create(ctx, testApp(fmt.Sprintf("dup%d", i), label), domain.MaxLaunchableApps)
		if !errors.Is(err, ErrConflict) {
			t.Errorf("Create %q = %v, want ErrConflict", label, err)
		}
	}
	// ASCII 以外は畳み込まない: 全角英字・Unicode の大文字小文字は別の値（NOCASE は ASCII のみ）。
	for i, label := range []string{"Ｎotepad", "ＮＯＴＥＰＡＤ", "Notepad "} {
		if err := s.Create(ctx, testApp(fmt.Sprintf("ok%d", i), label), domain.MaxLaunchableApps); err != nil {
			t.Errorf("Create %q = %v, want nil (distinct value)", label, err)
		}
	}
	if err := s.Create(ctx, testApp("u1", "Ärger"), domain.MaxLaunchableApps); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, testApp("u2", "ärger"), domain.MaxLaunchableApps); err != nil {
		t.Errorf("Create non-ASCII case variant = %v, want nil", err)
	}
	if err := s.Create(ctx, testApp("a1", "Different"), domain.MaxLaunchableApps); !errors.Is(err, ErrConflict) {
		t.Errorf("Create duplicate id = %v, want ErrConflict", err)
	}
}

func TestLaunchableAppsLimit(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	const limit = 3
	for i := range limit {
		if err := s.Create(ctx, testApp(fmt.Sprintf("a%d", i), fmt.Sprintf("app%d", i)), limit); err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
	}
	err := s.Create(ctx, testApp("over", "over"), limit)
	if !errors.Is(err, ErrLimitReached) {
		t.Fatalf("Create past the limit = %v, want ErrLimitReached", err)
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		t.Errorf("ErrLimitReached must be distinct from ErrConflict / ErrNotFound: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM launchable_apps`); n != limit {
		t.Errorf("rows = %d, want %d (the rejected row must not be inserted)", n, limit)
	}

	// 上限に達していても、label の重複でない削除・更新は通り、削除すれば再び登録できる。
	if err := s.Delete(ctx, "a0"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, testApp("again", "again"), limit); err != nil {
		t.Errorf("Create after delete = %v", err)
	}

	// 上限の判定は引数の max による。
	if err := s.Create(ctx, testApp("zero", "zero"), 0); !errors.Is(err, ErrLimitReached) {
		t.Errorf("Create with max 0 = %v, want ErrLimitReached", err)
	}
}

func TestLaunchableAppsDefaultLimitIsFifty(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	for i := range domain.MaxLaunchableApps {
		if err := s.Create(ctx, testApp(fmt.Sprintf("a%02d", i), fmt.Sprintf("app%02d", i)), domain.MaxLaunchableApps); err != nil {
			t.Fatalf("Create #%d: %v", i+1, err)
		}
	}
	if err := s.Create(ctx, testApp("a51", "app51"), domain.MaxLaunchableApps); !errors.Is(err, ErrLimitReached) {
		t.Errorf("51st Create = %v, want ErrLimitReached", err)
	}
}

// 失敗した Create（上限・重複）は接続を握ったままにならず、行も残さない（接続は 1 本。ROLLBACK されている）。
func TestLaunchableAppsFailedCreateRollsBack(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	if err := s.Create(ctx, testApp("a1", "one"), 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, testApp("a2", "two"), 1); !errors.Is(err, ErrLimitReached) {
		t.Fatal(err)
	}
	if err := s.Create(ctx, testApp("a3", "ONE"), 2); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Create(ctx, testApp("a4", "four"), 2) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Create after failures = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Create blocked after failed Create: transaction was not finished")
	}
	if got := appLabels(t, s); !reflect.DeepEqual(got, []string{"four", "one"}) {
		t.Errorf("labels = %q", got)
	}
}

// 件数の確認と挿入が同一トランザクションなので、並行する Create が上限を超えて通らない。
func TestLaunchableAppsConcurrentCreateRespectsLimit(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	const limit, workers = 5, 20
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		ok, full int
	)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Create(ctx, testApp(fmt.Sprintf("a%02d", i), fmt.Sprintf("app%02d", i)), limit)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrLimitReached):
				full++
			default:
				t.Errorf("Create: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != limit || full != workers-limit {
		t.Errorf("ok = %d, full = %d, want %d and %d", ok, full, limit, workers-limit)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM launchable_apps`); n != limit {
		t.Errorf("rows = %d, want %d", n, limit)
	}
}

func TestLaunchableAppsUpdate(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	orig := testApp("a1", "Notepad")
	if err := s.Create(ctx, orig, domain.MaxLaunchableApps); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, testApp("a2", "Other"), domain.MaxLaunchableApps); err != nil {
		t.Fatal(err)
	}

	upd := domain.LaunchableApp{
		ID: "a1", Label: "Notepad 2", ExePath: `D:\Tools\np.exe`, Args: nil, WorkingDir: "",
		CreatedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), // 無視される
		UpdatedAt: time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC),
	}
	if err := s.Update(ctx, upd); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	want := upd
	want.Args = []string{}
	want.CreatedAt = orig.CreatedAt // ID と作成時刻は変わらない
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after Update = %+v, want %+v", got, want)
	}

	// 自分自身の label を（大文字小文字だけ変えて）保つのは重複ではない。
	upd.Label = "NOTEPAD 2"
	if err := s.Update(ctx, upd); err != nil {
		t.Errorf("Update own label in different case = %v", err)
	}
	// 他の行と同じ label（ASCII の大文字小文字違い）は ErrConflict で、行は変わらない。
	upd.Label = "other"
	if err := s.Update(ctx, upd); !errors.Is(err, ErrConflict) {
		t.Errorf("Update to a duplicate label = %v, want ErrConflict", err)
	}
	if got, _ := s.Get(ctx, "a1"); got.Label != "NOTEPAD 2" {
		t.Errorf("label after failed Update = %q", got.Label)
	}

	upd.ID = "missing"
	upd.Label = "fresh"
	if err := s.Update(ctx, upd); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update missing = %v, want ErrNotFound", err)
	}
}

func TestLaunchableAppsDelete(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	if err := s.Create(ctx, testApp("a1", "x"), domain.MaxLaunchableApps); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
	// 削除した label は再び使える。
	if err := s.Create(ctx, testApp("a2", "x"), domain.MaxLaunchableApps); err != nil {
		t.Errorf("Create with a freed label = %v", err)
	}
}

// 手で書き換えられて読めない行（配列だが要素が文字列でない）は、Get・List を失敗させず、Corrupt を立てて Args を空で返す
// （パス・引数は残さない）。起動は domain.CheckLaunchTarget が拒否し、ホスト所有者は削除できる。
func TestLaunchableAppsUnreadableArgsIsMarkedCorrupt(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	s := db.LaunchableApps()

	for _, id := range []string{"a1", "a2"} {
		if err := s.Create(ctx, testApp(id, id), domain.MaxLaunchableApps); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{`[1]`, `[1, {"secret": "hunter2"}]`, `[["nested"]]`} {
		mustExec(t, db, `UPDATE launchable_apps SET args_json = '`+strings.ReplaceAll(bad, `'`, `''`)+`' WHERE id = 'a1'`)

		got, err := s.Get(ctx, "a1")
		if err != nil {
			t.Fatalf("Get(%s) = %v, want the row marked corrupt", bad, err)
		}
		if !got.Corrupt || len(got.Args) != 0 || got.Args == nil {
			t.Errorf("Get(%s) = Corrupt %v Args %#v, want Corrupt with empty non-nil Args", bad, got.Corrupt, got.Args)
		}
		if err := domain.CheckLaunchTarget(got); !errors.Is(err, domain.ErrAppNotFound) {
			t.Errorf("CheckLaunchTarget(%s) = %v, want ErrAppNotFound", bad, err)
		}

		list, err := s.List(ctx)
		if err != nil {
			t.Fatalf("List(%s) = %v, want success", bad, err)
		}
		if len(list) != 2 || !list[0].Corrupt || list[1].Corrupt {
			t.Errorf("List(%s) = %+v, want [a1 (corrupt), a2]", bad, list)
		}
	}

	// 読める行は Corrupt でない。
	if got, err := s.Get(ctx, "a2"); err != nil || got.Corrupt {
		t.Errorf("Get(a2) = %+v, %v, want a healthy row", got, err)
	}
	// 所有者は壊れた行を削除できる。
	if err := s.Delete(ctx, "a1"); err != nil {
		t.Fatalf("Delete of a corrupt row = %v", err)
	}
	if _, err := s.Get(ctx, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
}

// 起動許可リストは他の表に依存しない: クライアントの削除で消えない。
func TestLaunchableAppsAreIndependentOfClients(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)

	mustExec(t, db, `INSERT INTO clients (id, stable_node_id, login_name, display_name, created_at, updated_at)
		VALUES ('c1', 'node1', 'a@example.com', 'dev', 1, 1)`)
	if err := db.LaunchableApps().Create(ctx, testApp("a1", "x"), domain.MaxLaunchableApps); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `DELETE FROM clients`)
	if _, err := db.LaunchableApps().Get(ctx, "a1"); err != nil {
		t.Errorf("app disappeared with its clients: %v", err)
	}
}
