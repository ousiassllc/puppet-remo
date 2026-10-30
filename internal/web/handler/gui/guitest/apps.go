package guitest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// Apps は gui.Apps のインメモリ実装。本物（service.AppRegistry）と同じ規則を持つ: 形の検証は domain.ValidateAppInput、
// 表示名の重複（大文字小文字違いを含む）は項目エラー、50 件に達していれば conflict、無い id は ErrNotFound、一覧は表示名の昇順。
// 実行ファイルの存在は Missing に入れたパスだけが「無い」（それ以外は有る）。監査は記録しない（それは service の責務で、
// 結線した本物の記録は internal/app のテストが確かめる）。
type Apps struct {
	mu   sync.Mutex
	next int
	Rows []domain.LaunchableApp
	// Missing に入れた実行ファイルは存在しない（exe_path のエラーになる）。
	Missing map[string]bool
	// 呼び出しの記録。
	Created  []domain.AppInput
	Updated  map[string]domain.AppInput
	Deleted  []string
	Picks    int
	ListErr  error
	WriteErr error // 非 nil なら Create / Update / Delete がこれを返す

	// PickFunc は PickExecutable の振る舞い。nil なら、選択もキャンセルもせずにキャンセル（ok=false）を返す。
	PickFunc func(ctx context.Context) (path string, ok bool, err error)
}

func (a *Apps) List(context.Context) ([]domain.LaunchableApp, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ListErr != nil {
		return nil, a.ListErr
	}
	out := slices.Clone(a.Rows)
	slices.SortFunc(out, func(x, y domain.LaunchableApp) int {
		return strings.Compare(strings.ToLower(x.Label), strings.ToLower(y.Label))
	})
	return out, nil
}

func (a *Apps) find(id string) int {
	return slices.IndexFunc(a.Rows, func(r domain.LaunchableApp) bool { return r.ID == id })
}

func notFound() error { return fmt.Errorf("launchable app: %w", service.ErrNotFound) }

func (a *Apps) Get(_ context.Context, id string) (domain.LaunchableApp, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i := a.find(id); i >= 0 {
		return a.Rows[i], nil
	}
	return domain.LaunchableApp{}, notFound()
}

// check は形と存在と表示名の重複を確かめる。self は更新の対象の id（自分自身との重複は許す）。
func (a *Apps) check(in domain.AppInput, self string) error {
	errs := domain.ValidateAppInput(in)
	if errs == nil {
		errs = map[string]string{}
	}
	if _, bad := errs[domain.AppFieldExePath]; !bad && a.Missing[in.ExePath] {
		errs[domain.AppFieldExePath] = "実行ファイルが見つかりません"
	}
	label := strings.ToLower(strings.TrimSpace(in.Label))
	for _, r := range a.Rows {
		if r.ID != self && strings.ToLower(r.Label) == label {
			errs[domain.AppFieldLabel] = "同じ表示名のアプリがすでに登録されています"
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return service.AppFieldErrors(errs)
}

func (a *Apps) Create(_ context.Context, in domain.AppInput) (domain.LaunchableApp, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.WriteErr != nil {
		return domain.LaunchableApp{}, a.WriteErr
	}
	in.Label = strings.TrimSpace(in.Label)
	if err := a.check(in, ""); err != nil {
		return domain.LaunchableApp{}, err
	}
	if len(a.Rows) >= domain.MaxLaunchableApps {
		return domain.LaunchableApp{}, domain.NewError(domain.CodeConflict, "the launchable app list is full", nil)
	}
	a.next++
	row := domain.LaunchableApp{ID: fmt.Sprintf("app%03d", a.next), Label: in.Label, ExePath: in.ExePath, Args: in.Args, WorkingDir: in.WorkingDir}
	a.Rows = append(a.Rows, row)
	a.Created = append(a.Created, in)
	return row, nil
}

func (a *Apps) Update(_ context.Context, id string, in domain.AppInput) (domain.LaunchableApp, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.WriteErr != nil {
		return domain.LaunchableApp{}, a.WriteErr
	}
	i := a.find(id)
	if i < 0 {
		return domain.LaunchableApp{}, notFound()
	}
	in.Label = strings.TrimSpace(in.Label)
	if err := a.check(in, id); err != nil {
		return domain.LaunchableApp{}, err
	}
	a.Rows[i].Label, a.Rows[i].ExePath, a.Rows[i].Args, a.Rows[i].WorkingDir = in.Label, in.ExePath, in.Args, in.WorkingDir
	if a.Updated == nil {
		a.Updated = map[string]domain.AppInput{}
	}
	a.Updated[id] = in
	return a.Rows[i], nil
}

func (a *Apps) Delete(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.WriteErr != nil {
		return a.WriteErr
	}
	i := a.find(id)
	if i < 0 {
		return notFound()
	}
	a.Rows = slices.Delete(a.Rows, i, i+1)
	a.Deleted = append(a.Deleted, id)
	return nil
}

func (a *Apps) PickExecutable(ctx context.Context) (string, bool, error) {
	a.mu.Lock()
	a.Picks++
	f := a.PickFunc
	a.mu.Unlock()
	if f == nil {
		return "", false, nil
	}
	return f(ctx)
}
