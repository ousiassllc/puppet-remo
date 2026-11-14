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
	// Mode は起動の方式（空は allowlist）、Denied は拒否リスト、ModeSets は SetLaunchMode の呼び出しの記録。
	Mode     domain.LaunchMode
	Denied   []domain.DeniedApp
	ModeSets []domain.LaunchMode
	// 呼び出しの記録。
	Created  []domain.AppInput
	Updated  map[string]domain.AppInput
	Deleted  []string
	Picks    int
	ListErr  error
	WriteErr error // 非 nil なら Create / Update / Delete がこれを返す
	// ModeSwitchedThenErr が非 nil なら SetLaunchMode は、方式を切り替えたうえでこのエラーを返す
	// （監査に書けず、戻す書き込みも失敗した service.AppRegistry の再現）。
	ModeSwitchedThenErr error

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

// 起動の方式と拒否リスト（#88）。本物（service.AppRegistry）と同じ規則: 方式の既定は allowlist、不正な方式・形の不備
// （domain.ClassifyDenied）・重複（domain.DeniedKey）は項目エラー、200 件に達していれば conflict、無い id は ErrNotFound。
// パスの解決（実体のパス）と監査は service の責務で、ここでは入力をそのまま（区切りだけそろえて）保存する。

// LaunchMode は現在の方式。空なら allowlist。
func (a *Apps) LaunchMode(context.Context) (domain.LaunchMode, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Mode == "" {
		return domain.LaunchModeAllowlist, nil
	}
	return a.Mode, nil
}

func (a *Apps) SetLaunchMode(_ context.Context, mode domain.LaunchMode) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.WriteErr != nil {
		return a.WriteErr
	}
	if !mode.Valid() {
		return service.AppFieldErrors{domain.AppFieldLaunchMode: "起動の方式は「ホワイトリスト」か「ブラックリスト」を選んでください"}
	}
	a.Mode = mode
	a.ModeSets = append(a.ModeSets, mode)
	return a.ModeSwitchedThenErr
}

func (a *Apps) ListDenied(context.Context) ([]domain.DeniedApp, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ListErr != nil {
		return nil, a.ListErr
	}
	return slices.Clone(a.Denied), nil
}

func (a *Apps) findDenied(id string) int {
	return slices.IndexFunc(a.Denied, func(r domain.DeniedApp) bool { return r.ID == id })
}

// checkDenied は value の形と重複を確かめ、保存する Kind・Value を返す。self は更新の対象の id。
func (a *Apps) checkDenied(value, self string) (domain.DeniedKind, string, error) {
	value = strings.TrimSpace(value)
	kind, msg := domain.ClassifyDenied(value)
	if msg != "" {
		return "", "", service.AppFieldErrors{domain.AppFieldDeniedValue: msg}
	}
	if kind == domain.DeniedPath {
		value = domain.NormalizePathForMatch(value)
	}
	for _, r := range a.Denied {
		if r.ID != self && domain.DeniedKey(r.Kind, r.Value) == domain.DeniedKey(kind, value) {
			return "", "", service.AppFieldErrors{domain.AppFieldDeniedValue: "同じ実行ファイル（パスまたは名前）がすでに拒否リストに登録されています"}
		}
	}
	return kind, value, nil
}

func (a *Apps) CreateDenied(_ context.Context, value string) (domain.DeniedApp, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.WriteErr != nil {
		return domain.DeniedApp{}, a.WriteErr
	}
	kind, v, err := a.checkDenied(value, "")
	if err != nil {
		return domain.DeniedApp{}, err
	}
	if len(a.Denied) >= domain.MaxDeniedApps {
		return domain.DeniedApp{}, domain.NewError(domain.CodeConflict, "the launch denylist is full", nil)
	}
	a.next++
	row := domain.DeniedApp{ID: fmt.Sprintf("den%03d", a.next), Kind: kind, Value: v}
	a.Denied = append(a.Denied, row)
	return row, nil
}

func (a *Apps) UpdateDenied(_ context.Context, id, value string) (domain.DeniedApp, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.WriteErr != nil {
		return domain.DeniedApp{}, a.WriteErr
	}
	i := a.findDenied(id)
	if i < 0 {
		return domain.DeniedApp{}, notFound()
	}
	kind, v, err := a.checkDenied(value, id)
	if err != nil {
		return domain.DeniedApp{}, err
	}
	a.Denied[i].Kind, a.Denied[i].Value = kind, v
	return a.Denied[i], nil
}

func (a *Apps) DeleteDenied(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.WriteErr != nil {
		return a.WriteErr
	}
	i := a.findDenied(id)
	if i < 0 {
		return notFound()
	}
	a.Denied = slices.Delete(a.Denied, i, i+1)
	return nil
}
