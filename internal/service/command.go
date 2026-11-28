package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// CommandExec は、ホストの許可リストにあるコマンドだけを実行して結果を返す窓口（#265。MCP の run_command / list_commands。FR-M32）。
// platform.CommandRunner を呼ぶのはここだけ。どの goroutine からも呼べる。
//
// Run の検査の順序は固定で、1 つでも失敗したら何も起動しない:
//
//  1. KillSwitch → ErrKillSwitchActive
//  2. ホストの設定 command.enabled（既定 false）が false → ErrCommandDisabled（リースを取りに行かない）
//  3. LeaseManager.Check（リース未保持の mcp は暗黙に Acquire を試みる。承認が要るクライアントは承認を待つ。FR-L04）。
//     ウィンドウに束縛されたリース（#156）では使えない（ErrInputBlocked、reason command_out_of_scope）
//  4. 許可リストのファイル（設定 command.allowlist_path。毎回読み直す）の読み込みと検査。壊れていれば ErrCommandAllowlistInvalid
//     （全項目が使えない。理由は運用ログにだけ書く）。許可リストのファイル・実行ファイル・作業フォルダが、リモートが書けるファイル転送の
//     置き場所の中にあれば同じく ErrCommandAllowlistInvalid（リモートが差し替えられるため）
//  5. 名前の照合（無い・無効の項目は ErrCommandNotFound）と引数の検査（domain.CommandEntry.Bind。ErrInvalidArgument）。
//     inbox_path は FileTransfer.ResolveInRoot で実体にする
//  6. KillSwitch の再確認と platform.CommandRunner の実行（同時に走らせる数に上限。超えれば ErrConflict）
//  7. 監査（tool.run_command。項目名・引数の値〔長さを切り詰める〕・終了コード・出力の大きさ・結果。出力の中身は記録しない。FR-AU03）
//
// ホストの入力の一時停止（#270）は見ない（入力を注入しないため）。許可リスト・設定を MCP から変える経路は無い。
type CommandExec interface {
	// Run は name の項目を args で実行する。非 0 の終了コード・タイムアウト・出力の切り詰めはエラーではなく結果で返す。
	Run(ctx context.Context, who domain.Actor, req CommandRunRequest) (CommandRunResult, error)
	// List は実行できる（有効な）項目と、受け取る引数の定義を、名前の昇順で返す。リースは要らない（キルスイッチと ON/OFF は見る）。
	// 実行ファイルのパス・固定の引数は返さない。
	List(ctx context.Context, who domain.Actor) ([]CommandInfo, error)
	// CancelAll は実行中のコマンドをすべて終了させる（キルスイッチの発動。実行中の Run は ErrKillSwitchActive で返る）。
	CancelAll(ctx context.Context) error
}

// CommandRunRequest は CommandExec.Run の指定。Args は JSON から来た値（文字列・数値）の対応表。
type CommandRunRequest struct {
	Name string
	Args map[string]any
}

// CommandRunResult は Run の結果。
type CommandRunResult struct {
	Name            string
	ExitCode        int
	Stdout, Stderr  []byte
	StdoutTruncated bool
	StderrTruncated bool
	TimedOut        bool
	Duration        time.Duration
}

// CommandInfo は List の 1 項目。
type CommandInfo struct {
	Name           string
	Description    string
	Params         []domain.CommandParam
	Timeout        time.Duration
	MaxOutputBytes int
}

// maxConcurrentCommands は同時に走らせられるコマンドの数。
const maxConcurrentCommands = 4

// 監査に残す、呼び出し側が渡した値の長さの上限。
const (
	maxAuditCommandArgs       = 16
	maxAuditCommandKeyRunes   = 64
	maxAuditCommandValueRunes = 128
)

// CommandExecConfig は NewCommandExec の設定。Runner・Files・Leases・Kill・Clock・DefaultAllowlistPath・DefaultInboxRoot は必須。
type CommandExecConfig struct {
	Runner platform.CommandRunner
	// Files は inbox_path の解決（FileTransfer.ResolveInRoot）。
	Files  FileTransfer
	Leases LeaseManager
	Kill   KillSwitchState
	// Settings は command.* と file_transfer.root の読み取り。nil なら常に OFF。
	Settings repository.Settings
	// DefaultAllowlistPath は設定 command.allowlist_path が空のときの許可リストの場所。domain.ValidateHostFilePath を満たすこと。
	DefaultAllowlistPath string
	// DefaultInboxRoot は FileTransferConfig.DefaultRoot と同じ値（ファイル転送の置き場所の既定。許可リストなどがその中にないことの確認に使う）。
	DefaultInboxRoot string
	Audit            AuditRecorder // nil なら記録しない
	Clock            domain.Clock
	Logger           *slog.Logger // nil なら slog.Default()
}

type commandExec struct {
	cfg CommandExecConfig
	log *slog.Logger
	sem chan struct{}

	mu      sync.Mutex
	running map[int]context.CancelFunc
	nextID  int
}

// NewCommandExec は CommandExec を返す。
func NewCommandExec(cfg CommandExecConfig) (CommandExec, error) {
	if cfg.Runner == nil || cfg.Files == nil || cfg.Leases == nil || cfg.Kill == nil || cfg.Clock == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("command exec requires runner, files, leases, kill switch and clock")
	}
	if err := domain.ValidateHostFilePath(cfg.DefaultAllowlistPath); err != nil {
		return nil, err
	}
	if err := domain.ValidateTransferRoot(cfg.DefaultInboxRoot); err != nil {
		return nil, err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &commandExec{cfg: cfg, log: log, sem: make(chan struct{}, maxConcurrentCommands), running: map[int]context.CancelFunc{}}, nil
}

// gate は検査の 1・2（キルスイッチ・ON/OFF）を行い、設定を返す。
func (c *commandExec) gate(ctx context.Context) (commandSettings, error) {
	if c.cfg.Kill.Active() {
		return commandSettings{}, domain.ErrKillSwitchActive
	}
	set, err := loadCommandSettings(ctx, c.cfg.Settings)
	if err != nil {
		return commandSettings{}, err
	}
	if !set.Enabled {
		return commandSettings{}, domain.ErrCommandDisabled
	}
	return set, nil
}

// allowlist は許可リストのファイルを読み込み、検査する（呼ぶたびに読み直す）。許可リストのファイルがファイル転送の置き場所の中にあれば拒否する。
// root はその置き場所を返す（実行ファイル・作業フォルダの確認にも使う）。
func (c *commandExec) allowlist(ctx context.Context, set commandSettings) (list domain.CommandAllowlist, root string, err error) {
	root, err = loadFileTransferRoot(ctx, c.cfg.Settings)
	if err != nil {
		return list, "", err
	}
	if root == "" {
		root = c.cfg.DefaultInboxRoot
	}
	path := set.AllowlistPath
	if path == "" {
		path = c.cfg.DefaultAllowlistPath
	}
	if within(root, path) {
		c.log.Warn("the command allowlist file is inside the file transfer folder, so commands are disabled (remote clients can write there)")
		return list, root, domain.ErrCommandAllowlistInvalid
	}
	data, err := readAllowlistFile(path)
	if err != nil {
		c.log.Warn("cannot read the command allowlist file", "error", err)
		return list, root, domain.ErrCommandAllowlistInvalid
	}
	if list, err = domain.ParseCommandAllowlist(data); err != nil {
		c.log.Warn("invalid command allowlist; no command can run until it is fixed", "error", err)
		return domain.CommandAllowlist{}, root, domain.ErrCommandAllowlistInvalid
	}
	return list, root, nil
}

// readAllowlistFile は通常ファイルを、大きさの上限つきで読む。
func readAllowlistFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("the allowlist is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, domain.MaxCommandFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > domain.MaxCommandFileBytes {
		return nil, errors.New("the allowlist file is too large")
	}
	return data, nil
}

func (c *commandExec) List(ctx context.Context, who domain.Actor) (infos []CommandInfo, err error) {
	start := c.cfg.Clock.Now()
	detail := map[string]any{}
	defer func() {
		if err == nil {
			detail["count"] = len(infos)
		}
		c.record(ctx, who, "list_commands", detail, "", start, err)
	}()
	set, err := c.gate(ctx)
	if err != nil {
		return nil, err
	}
	list, root, err := c.allowlist(ctx, set)
	if err != nil {
		return nil, err
	}
	infos = []CommandInfo{}
	for _, e := range list.Entries {
		if !e.Enabled || !entryOutsideRoot(e, root) {
			continue
		}
		infos = append(infos, CommandInfo{Name: e.Name, Description: e.Description, Params: e.Params, Timeout: e.Timeout, MaxOutputBytes: e.MaxOutputBytes})
	}
	return infos, nil
}

// entryOutsideRoot は実行ファイル・作業フォルダが、リモートが書けるファイル転送の置き場所の中にないこと
// （置き場所の外のフォルダの中に置き場所があるのは構わない）。
func entryOutsideRoot(e domain.CommandEntry, root string) bool {
	return !within(root, e.Executable) && (e.WorkingDir == "" || !within(root, e.WorkingDir))
}

func (c *commandExec) Run(ctx context.Context, who domain.Actor, req CommandRunRequest) (res CommandRunResult, err error) {
	start := c.cfg.Clock.Now()
	detail := map[string]any{"name": domain.TruncateRunes(req.Name, maxAuditCommandKeyRunes), "args": auditCommandArgs(req.Args)}
	var leaseID string
	defer func() {
		if err == nil {
			detail["exit_code"], detail["timed_out"] = res.ExitCode, res.TimedOut
			detail["stdout_bytes"], detail["stderr_bytes"] = len(res.Stdout), len(res.Stderr)
			detail["stdout_truncated"], detail["stderr_truncated"] = res.StdoutTruncated, res.StderrTruncated
		}
		c.record(ctx, who, "run_command", detail, leaseID, start, err)
	}()

	set, err := c.gate(ctx)
	if err != nil {
		return res, err
	}
	leaseID, scope, err := ensureLease(ctx, c.cfg.Leases, who)
	if err != nil {
		return res, err
	}
	if scope != nil {
		return res, scopeBlocked(domain.ReasonCommandOutOfScope, domain.BlockedBy{})
	}
	list, root, err := c.allowlist(ctx, set)
	if err != nil {
		return res, err
	}
	entry, ok := list.Entry(req.Name)
	if !ok || !entry.Enabled {
		return res, domain.ErrCommandNotFound
	}
	if !entryOutsideRoot(entry, root) {
		c.log.Warn("a command's executable or working directory is inside the file transfer folder, so it cannot run", "command", entry.Name)
		return res, domain.ErrCommandAllowlistInvalid
	}
	values, err := entry.Bind(req.Args)
	if err != nil {
		return res, err
	}
	for _, p := range entry.Params {
		if p.Type != domain.CommandParamInboxPath {
			continue
		}
		if values[p.Name], err = c.cfg.Files.ResolveInRoot(ctx, values[p.Name]); err != nil {
			return res, err
		}
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	default:
		return res, domain.NewError(domain.CodeConflict, "too many commands are running; try again when one has finished", nil)
	}
	if c.cfg.Kill.Active() {
		return res, domain.ErrKillSwitchActive
	}
	runCtx, cancel := context.WithCancel(ctx)
	id := c.track(cancel)
	defer func() { c.untrack(id); cancel() }()
	out, err := c.cfg.Runner.Run(runCtx, platform.CommandSpec{
		Executable: entry.Executable, Args: entry.Argv(values), Dir: entry.WorkingDir, Timeout: entry.Timeout, MaxOutputBytes: entry.MaxOutputBytes,
	})
	if err != nil {
		if ctx.Err() == nil && runCtx.Err() != nil {
			return res, domain.ErrKillSwitchActive // CancelAll が止めた
		}
		return res, err
	}
	return CommandRunResult{
		Name: entry.Name, ExitCode: out.ExitCode, Stdout: out.Stdout, Stderr: out.Stderr,
		StdoutTruncated: out.StdoutTruncated, StderrTruncated: out.StderrTruncated, TimedOut: out.TimedOut, Duration: out.Duration,
	}, nil
}

func (c *commandExec) track(cancel context.CancelFunc) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	c.running[c.nextID] = cancel
	return c.nextID
}

func (c *commandExec) untrack(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.running, id)
}

func (c *commandExec) CancelAll(context.Context) error {
	c.mu.Lock()
	cancels := slices.Collect(maps.Values(c.running))
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return nil
}

func (c *commandExec) record(ctx context.Context, who domain.Actor, tool string, detail map[string]any, leaseID string, start time.Time, err error) {
	if c.cfg.Audit == nil {
		return
	}
	entry := ToolAuditEntry(who, tool, detail, leaseID, err, c.cfg.Clock.Now().Sub(start))
	if domain.CodeOf(err) == domain.CodeInvalidArgument {
		entry.Result = auditDenied // 型・範囲・パターンに合わない引数・置き場所の外に出る指定は、拒否として残す
	}
	c.cfg.Audit.Record(ctx, entry)
}

// auditCommandArgs は呼び出し側が渡した（検査の前の）引数を、監査に残せる形にする: 数と長さを切り詰め、文字列・数値以外は型名だけにする。
func auditCommandArgs(args map[string]any) map[string]any {
	out := map[string]any{}
	for i, k := range slices.Sorted(maps.Keys(args)) {
		if i >= maxAuditCommandArgs {
			out["…"] = fmt.Sprintf("%d more", len(args)-maxAuditCommandArgs)
			break
		}
		key := domain.TruncateRunes(k, maxAuditCommandKeyRunes)
		switch v := args[k].(type) {
		case string:
			out[key] = domain.TruncateRunes(v, maxAuditCommandValueRunes)
		case float64, int, int64:
			out[key] = v
		default:
			out[key] = fmt.Sprintf("<%T>", v)
		}
	}
	return out
}
