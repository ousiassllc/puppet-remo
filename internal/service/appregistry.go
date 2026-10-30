package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// AppReader は許可リストの読み取り専用の面。Control はこれだけを受け取る（書き込みは持てない。NFR-S20）。AppRegistry が満たす。
// 読めない行（args_json が壊れている）は Corrupt を立てて返し、エラーにしない（domain.LaunchableApp.Corrupt）。
type AppReader interface {
	// List は許可リストを label の昇順で返す（パス・引数を含む。GUI 用）。
	List(ctx context.Context) ([]domain.LaunchableApp, error)
	// Get は id の行を返す。無ければ ErrNotFound。
	Get(ctx context.Context, id string) (domain.LaunchableApp, error)
}

// AppRegistry は起動許可リストの管理（M1.5。FR-M18・M19・H12。docs/components/overview.md §5.4.1）。
// 書き込み（Create / Update / Delete）を呼ぶのはホスト GUI だけで、mcp・リモートルーターには渡さない（NFR-S20）。
// Control は読み取り（Get / List）に使う。ステートレスで、許可リストの正は repository.LaunchableApps。
// HostAdmin には統合しない（依存が重ならないため）。M4 の Agent も同じ AppRegistry を使う。
//
// 書き込みはホスト所有者の操作として actor_type = host で監査する（settings.launchable_app.create / update / delete）。
// 引数と作業ディレクトリはどのイベントにも記録しない（arg_count だけ。FR-AU06）。
type AppRegistry interface {
	AppReader
	// Create は domain.ValidateAppInput と AppLauncher.Inspect（存在確認）を通った in を保存する。
	// 入力の不備・存在しないパスは AppFieldErrors、label の重複も AppFieldErrors{"label": …}、
	// 50 件に達していれば domain.CodeConflict。
	Create(ctx context.Context, in domain.AppInput) (domain.LaunchableApp, error)
	// Update は id の行を in で置き換える（id・CreatedAt は変えない）。検証は Create と同じ。無ければ ErrNotFound。
	Update(ctx context.Context, id string, in domain.AppInput) (domain.LaunchableApp, error)
	// Delete は id の行を消す。無ければ ErrNotFound。起動済みのプロセスには触れない。
	Delete(ctx context.Context, id string) error
	// PickExecutable はネイティブのファイル選択ダイアログを開く（platform.FilePicker の委譲）。キャンセルは ok=false。
	PickExecutable(ctx context.Context) (path string, ok bool, err error)
}

// AppFieldErrors は登録・更新フォームの項目ごとのエラー（項目名 → メッセージ。項目名は domain.AppField*）。
// GUI が 422 で再描画する。メッセージに入力値（パス・引数）は含まれない。
type AppFieldErrors map[string]string

// Error は項目名だけを並べる（メッセージもパスも含めない）。
func (e AppFieldErrors) Error() string {
	fields := make([]string, 0, len(e))
	for f := range e {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	return "service: invalid launchable app: " + strings.Join(fields, ", ")
}

const (
	msgAppExeMissing        = "実行ファイルが見つかりません"
	msgAppWorkingDirMissing = "作業ディレクトリが見つかりません"
	msgAppLabelTaken        = "同じ表示名のアプリがすでに登録されています"
)

// AppRegistryConfig は NewAppRegistry の設定。Audit 以外は必須。
type AppRegistryConfig struct {
	Apps repository.LaunchableApps
	// Launcher は存在確認（Inspect）にだけ使う。起動（Launch）は Control だけが呼ぶ。
	Launcher platform.AppLauncher
	Picker   platform.FilePicker
	Audit    AuditRecorder // nil なら記録しない
	Clock    domain.Clock
	IDs      domain.IDGen
}

type appRegistry struct{ cfg AppRegistryConfig }

// NewAppRegistry は AppRegistry を返す。
func NewAppRegistry(cfg AppRegistryConfig) (AppRegistry, error) {
	if cfg.Apps == nil || cfg.Launcher == nil || cfg.Picker == nil || cfg.Clock == nil || cfg.IDs == nil {
		return nil, errors.New("service: app registry requires Apps, Launcher, Picker, Clock and IDs")
	}
	return &appRegistry{cfg: cfg}, nil
}

func (r *appRegistry) List(ctx context.Context) ([]domain.LaunchableApp, error) {
	return r.cfg.Apps.List(ctx)
}

func (r *appRegistry) Get(ctx context.Context, id string) (domain.LaunchableApp, error) {
	a, err := r.cfg.Apps.Get(ctx, id)
	return a, mapAppErr(err)
}

func (r *appRegistry) PickExecutable(ctx context.Context) (string, bool, error) {
	return r.cfg.Picker.PickExecutable(ctx)
}

func (r *appRegistry) Create(ctx context.Context, in domain.AppInput) (domain.LaunchableApp, error) {
	in = normalizeAppInput(in)
	if err := r.check(ctx, in); err != nil {
		return domain.LaunchableApp{}, err
	}
	now := r.cfg.Clock.Now()
	a := domain.LaunchableApp{
		ID: r.cfg.IDs.NewID(), Label: in.Label, ExePath: in.ExePath, Args: in.Args, WorkingDir: in.WorkingDir,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := r.cfg.Apps.Create(ctx, a, domain.MaxLaunchableApps); err != nil {
		return domain.LaunchableApp{}, mapAppWriteErr(err)
	}
	r.audit(ctx, "settings.launchable_app.create", map[string]any{
		"app_id": a.ID, "label": a.Label, "exe_path": a.ExePath, "arg_count": len(a.Args),
	})
	return a, nil
}

func (r *appRegistry) Update(ctx context.Context, id string, in domain.AppInput) (domain.LaunchableApp, error) {
	before, err := r.Get(ctx, id)
	if err != nil {
		return domain.LaunchableApp{}, err
	}
	in = normalizeAppInput(in)
	if err := r.check(ctx, in); err != nil {
		return domain.LaunchableApp{}, err
	}
	a := before
	a.Label, a.ExePath, a.Args, a.WorkingDir = in.Label, in.ExePath, in.Args, in.WorkingDir
	a.Corrupt = false // 読めなかった args は入力で置き換わる
	a.UpdatedAt = r.cfg.Clock.Now()
	if err := r.cfg.Apps.Update(ctx, a); err != nil {
		return domain.LaunchableApp{}, mapAppWriteErr(err)
	}
	r.audit(ctx, "settings.launchable_app.update", map[string]any{
		"app_id": a.ID, "label": a.Label, "exe_path": a.ExePath, "arg_count": len(a.Args),
		"changed": changedAppFields(before, a),
	})
	return a, nil
}

func (r *appRegistry) Delete(ctx context.Context, id string) error {
	a, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := r.cfg.Apps.Delete(ctx, id); err != nil {
		return mapAppErr(err)
	}
	r.audit(ctx, "settings.launchable_app.delete", map[string]any{"app_id": a.ID, "label": a.Label})
	return nil
}

// normalizeAppInput は保存する形にする: 表示名の前後の空白を除き、引数は非 nil にする（複製して呼び出し側と共有しない）。
func normalizeAppInput(in domain.AppInput) domain.AppInput {
	in.Label = strings.TrimSpace(in.Label)
	in.Args = append([]string{}, in.Args...)
	return in
}

// check は in の形（domain.ValidateAppInput）と、実行ファイル・作業ディレクトリの存在（AppLauncher.Inspect）を確かめる。
// 形が不正な項目のパスは Inspect に渡さない（作業ディレクトリは、実行ファイルが存在するときだけ確かめる。
// Inspect はどちらが無いかを区別しないため）。不備があれば AppFieldErrors を返す。
func (r *appRegistry) check(ctx context.Context, in domain.AppInput) error {
	errs := domain.ValidateAppInput(in)
	if errs == nil {
		errs = map[string]string{}
	}
	_, exeBad := errs[domain.AppFieldExePath]
	if !exeBad {
		err := r.cfg.Launcher.Inspect(ctx, in.ExePath, "")
		switch {
		case errors.Is(err, platform.ErrNotFound):
			errs[domain.AppFieldExePath] = msgAppExeMissing
			exeBad = true
		case err != nil:
			return err
		}
	}
	if _, dirBad := errs[domain.AppFieldWorkingDir]; !exeBad && !dirBad && in.WorkingDir != "" {
		err := r.cfg.Launcher.Inspect(ctx, in.ExePath, in.WorkingDir)
		switch {
		case errors.Is(err, platform.ErrNotFound):
			errs[domain.AppFieldWorkingDir] = msgAppWorkingDirMissing
		case err != nil:
			return err
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return AppFieldErrors(errs)
}

// mapAppErr は repository の「無い」を ErrNotFound に写す。
func mapAppErr(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return errNotFound("launchable app")
	}
	return err
}

// mapAppWriteErr は保存の失敗を写す: label の重複は項目エラー、50 件到達は conflict、無いは ErrNotFound。
func mapAppWriteErr(err error) error {
	switch {
	case errors.Is(err, repository.ErrConflict):
		return AppFieldErrors{domain.AppFieldLabel: msgAppLabelTaken}
	case errors.Is(err, repository.ErrLimitReached):
		return domain.NewError(domain.CodeConflict, "the launchable app list is full", nil)
	}
	return mapAppErr(err)
}

// changedAppFields は before から after で変わった項目名を、固定の順（label・exe_path・args・working_dir）で返す。
// before が読めない行（Corrupt）のときは、壊れていた引数が入力で破棄されたことを監査に残すため、args を必ず含める。
func changedAppFields(before, after domain.LaunchableApp) []string {
	changed := []string{}
	if before.Label != after.Label {
		changed = append(changed, domain.AppFieldLabel)
	}
	if before.ExePath != after.ExePath {
		changed = append(changed, domain.AppFieldExePath)
	}
	if before.Corrupt || !slices.Equal(before.Args, after.Args) {
		changed = append(changed, domain.AppFieldArgs)
	}
	if before.WorkingDir != after.WorkingDir {
		changed = append(changed, domain.AppFieldWorkingDir)
	}
	return changed
}

// audit はホスト所有者の操作を actor_type = host で記録する。detail に引数・作業ディレクトリを入れてはならない。
func (r *appRegistry) audit(ctx context.Context, event string, detail map[string]any) {
	if r.cfg.Audit == nil {
		return
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		raw = nil
	}
	r.cfg.Audit.Record(ctx, repository.AuditEntry{
		TS:         r.cfg.Clock.Now(),
		Kind:       "settings",
		Event:      event,
		ActorType:  domain.ActorHost,
		Result:     auditOK,
		DetailJSON: raw,
	})
}
