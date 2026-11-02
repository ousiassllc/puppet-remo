package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aead.dev/minisign"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// 更新の確認の間隔（FR-H09「定期確認」）。起動の 1 分後に 1 回、以後 6 時間ごと。オフラインのときは失敗をログに残して続ける。
const (
	DefaultUpdateStartDelay = time.Minute
	DefaultUpdateInterval   = 6 * time.Hour
	// DefaultUpdateIdlePoll は auto_idle で、署名検証済みの新版が出ているのに適用できない（リース・接続がある）間の再試行の間隔。
	DefaultUpdateIdlePoll = 30 * time.Second
)

// 更新の監査（kind = update）。イベント名は docs/architecture/er.md のカタログと AuditCatalog に揃える。
const (
	updateAuditKind                   = "update"
	UpdateDetectedAuditEvent          = "update.detected"           // 署名検証に成功した新版を検出した
	UpdateVerifyFailedAuditEvent      = "update.verify_failed"      // 署名の検証・版の束縛・取得に失敗した（適用しない）
	UpdateDowngradeRejectedAuditEvent = "update.downgrade_rejected" // 現行より古い版（適用しない）
	UpdateApplyStartedAuditEvent      = "update.apply_started"      // インストーラを起動する（アプリは続けて終了する）
	UpdateApplyFailedAuditEvent       = "update.apply_failed"       // 適用に失敗した
)

// 適用できない理由（UpdateBusyError.Reasons）。利用者向けの文は UpdateBusyReasonText。
const (
	UpdateBusyLeaseHeld    = "lease_held"           // リース保持中
	UpdateBusyLeasePending = "lease_pending"        // リースの承認待ち
	UpdateBusySessions     = "sessions_connected"   // MCP・WebSocket のセッションが接続中
	UpdateBusyTasks        = "tasks_running"        // タスク実行中（M4）
	UpdateBusyApplying     = "applying_or_checking" // 適用中、または確認中
)

// UpdateBusyReasonText は適用できない理由の利用者向けの文。
func UpdateBusyReasonText(reason string) string {
	switch reason {
	case UpdateBusyLeaseHeld:
		return "操作権（リース）を保持しているクライアントがいます。"
	case UpdateBusyLeasePending:
		return "操作権の承認待ちの要求があります。"
	case UpdateBusySessions:
		return "接続中のクライアントがいます。"
	case UpdateBusyTasks:
		return "実行中のタスクがあります。"
	case UpdateBusyApplying:
		return "更新の確認または適用の処理中です。しばらくしてからもう一度お試しください。"
	}
	return "いまは適用できません。"
}

// UpdateBusyError は、いまは適用できない（アイドルでない・処理中）ことを表す。domain.CodeConflict（HTTP 409）として扱える。
type UpdateBusyError struct{ Reasons []string }

func (e *UpdateBusyError) Error() string {
	return "update cannot be applied now: " + strings.Join(e.Reasons, ",")
}

// Unwrap は domain.CodeOf が conflict と判定できるようにする。
func (e *UpdateBusyError) Unwrap() error {
	return domain.NewError(domain.CodeConflict, "update cannot be applied now", nil)
}

// ErrUpdateUnavailable は、適用できる（署名検証済みで新しい）更新が無いことを表す。domain.CodeConflict（HTTP 409）。
var ErrUpdateUnavailable = domain.NewError(domain.CodeConflict, "no verified update is available", nil)

// UpdateStatus は更新の状態（最新の確認の結果）。
type UpdateStatus struct {
	CurrentVersion string
	Channel        domain.Channel
	ApplyMode      string // UpdateApplyConfirm / UpdateApplyAutoIdle
	// Updatable は更新の対象のビルドか（dev・書式違いの版は false で、確認も適用もしない）。
	Updatable bool
	// KeyConfigured は署名の公開鍵が埋め込まれているか（false なら確認はするが適用しない）。
	KeyConfigured bool
	// LastCheckedAt は最後に確認した時刻（成功・失敗を問わない。ゼロなら未確認）。
	LastCheckedAt time.Time
	// LastError は最後の確認の失敗の分類（UpdateErr*。成功なら空）。
	LastError string
	// Latest は現行より新しい版（チャネルの対象）。無ければ nil。
	Latest *UpdateInfo
	// Applying は適用中（インストーラを起動してアプリの終了を待っている）。
	Applying bool
}

// UpdateInfo は見つかった新版。
type UpdateInfo struct {
	Version     string
	Tag         string
	Asset       string
	Notes       string // リリースノートの要約（外部由来。表示側が HTML エスケープする）
	PublishedAt time.Time
	// Verified は署名の検証に成功し、適用できる状態か。false なら RejectReason が理由。
	Verified     bool
	RejectReason string // UpdateReject*
}

// UpdateRejectDownloadFailed は、署名の検証まで進めなかった（インストーラ・署名の取得に失敗した）ことを表す。
const UpdateRejectDownloadFailed = "download_failed"

// Updater は更新の確認と適用（docs/components/overview.md §5.10）。UI を知らない。
// 状態は最新の確認の結果だけで、sync.Mutex で守る。ロックを持ったまま通信・Bus・監査・インストーラを呼ばない。
type Updater interface {
	// Status は今の状態を返す（通信しない）。
	Status(ctx context.Context) UpdateStatus
	// Check は GitHub Releases を確認し、現行より新しい版があれば署名を検証する。検証に成功した版だけを通知（Bus の toast）・適用の対象にする。
	// 通信の失敗・署名の不一致は状態（LastError・Latest）と運用ログ・監査に残し、エラーにはしない（アプリを止めない）。
	// by は監査の操作主体で、host（GUI の「今すぐ確認」）か system（定期確認）。dev ビルドは何もしない。
	Check(ctx context.Context, by domain.ActorKind) UpdateStatus
	// Apply は署名検証済みの新版を適用する（インストーラを `/S` で起動し、アプリを終了する）。
	// リース保持中・承認待ち・接続中・タスク実行中は *UpdateBusyError、適用できる更新が無ければ ErrUpdateUnavailable
	// （どちらも domain.CodeConflict）。公開鍵が未設定なら ErrUpdateKeyNotConfigured。
	Apply(ctx context.Context, by domain.ActorKind) error
	// Start は定期確認を始める。Close で止める。2 回目以降は何もしない。
	Start()
	// Close は定期確認を止め、進行中の処理を待ち、未適用の一時ファイルを消す。何度呼んでもよい。
	Close()
}

// UpdateLeaseState は適用の可否の判定に使う、リースの状態の読み取り（LeaseManager が満たす）。
type UpdateLeaseState interface {
	Holder() (Holder, bool)
	Pending() (PendingLease, bool)
}

// UpdaterConfig は NewUpdater の設定。Settings・Clock・Installer・Leases・Sessions・CurrentVersion は必須。
type UpdaterConfig struct {
	Settings repository.Settings
	Audit    AuditRecorder // nil なら記録しない
	Clock    domain.Clock
	// Installer は署名検証済みのインストーラの起動（platform。本番は Windows、テストは偽の実装）。
	Installer platform.Installer
	Leases    UpdateLeaseState
	Sessions  SessionSource
	// RunningTasks は実行中のタスクの数（M4）。nil なら常に 0。
	RunningTasks func() int
	// Bus は nil なら通知（toast）を出さない。
	Bus *Bus
	// Quit は適用の開始後にアプリを終える（インストーラが実行中のアプリのファイルを置き換えるため）。nil なら何もしない。
	// Apply の中で同期に呼ぶので、呼び出し元を待たせる実装は避ける（goroutine で終了を始める）。
	Quit func()

	// CurrentVersion は実行中の版（main.version）。dev・書式違いは更新の対象外。
	CurrentVersion string
	// PublicKey は署名の検証に使う公開鍵（minisign の .pub の中身。本番は EmbeddedUpdatePublicKey()）。
	// 空・コメントだけ（プレースホルダ）なら未設定として、確認はするが適用しない。書式が不正なら NewUpdater が失敗する。
	PublicKey []byte

	// Repo は owner/name（既定 DefaultUpdateRepo）。APIBase・DownloadBase は https の起点（既定は GitHub。テストは loopback の http も可）。
	Repo         string
	APIBase      string
	DownloadBase string
	// HTTPClient は nil なら既定のクライアント。リダイレクトの先は https の GitHub の配信元だけに制限する（APIBase・DownloadBase のホストも許す）。
	HTTPClient *http.Client
	// TempDir は検証済みのインストーラを置く場所（既定 os.TempDir()）。
	TempDir string

	// StartDelay・Interval・IdlePoll は 0 なら既定値。AfterFunc は nil なら time.AfterFunc。
	StartDelay time.Duration
	Interval   time.Duration
	IdlePoll   time.Duration
	AfterFunc  AfterFunc
	Logger     *slog.Logger // nil なら slog.Default()
}

type updater struct {
	cfg    UpdaterConfig
	log    *slog.Logger
	client *http.Client
	after  AfterFunc
	// maxInstaller はインストーラの大きさの上限（既定 maxInstallerBytes。テストが小さくする）。
	maxInstaller int64

	current   domain.Version
	updatable bool
	pub       minisign.PublicKey
	keyOK     bool

	// work は確認と適用を直列にする（容量 1。Apply が成功したら返さない）。
	work chan struct{}

	runCtx    context.Context
	runCancel context.CancelFunc
	wg        sync.WaitGroup

	mu          sync.Mutex
	started     bool
	closed      bool
	timer       func() bool // 定期確認のタイマーの Stop
	pollTimer   func() bool // auto_idle の再試行のタイマーの Stop
	lastChecked time.Time
	loaded      bool // lastChecked を設定から読んだか
	lastErr     string
	latest      *candidateState
	applying    bool
	auditedKey  string // 直前に監査した（イベント・版・理由）。同じ内容を定期確認のたびに記録しない
	notifiedTag string // 直前に通知（toast）した版
}

// candidateState は確認で見つけた新版と、検証済みのインストーラ（検証に成功したときだけ vi がある）。
type candidateState struct {
	cand    updateCandidate
	channel domain.Channel
	vi      *verifiedInstaller
	reject  string
}

// NewUpdater は Updater を作る。通信・goroutine はまだ無い（定期確認は Start が始める）。
func NewUpdater(cfg UpdaterConfig) (Updater, error) {
	switch {
	case cfg.Settings == nil, cfg.Clock == nil, cfg.Installer == nil, cfg.Leases == nil, cfg.Sessions == nil:
		return nil, errors.New("service: updater requires Settings, Clock, Installer, Leases and Sessions")
	case cfg.CurrentVersion == "":
		return nil, errors.New("service: updater requires CurrentVersion")
	}
	if cfg.Repo == "" {
		cfg.Repo = DefaultUpdateRepo
	}
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultUpdateAPIBase
	}
	if cfg.DownloadBase == "" {
		cfg.DownloadBase = DefaultUpdateDownloadBase
	}
	if cfg.StartDelay <= 0 {
		cfg.StartDelay = DefaultUpdateStartDelay
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultUpdateInterval
	}
	if cfg.IdlePoll <= 0 {
		cfg.IdlePoll = DefaultUpdateIdlePoll
	}
	if cfg.TempDir == "" {
		cfg.TempDir = os.TempDir()
	}
	u := &updater{cfg: cfg, log: cfg.Logger, after: cfg.AfterFunc, work: make(chan struct{}, 1), maxInstaller: maxInstallerBytes}
	if u.log == nil {
		u.log = slog.Default()
	}
	if u.after == nil {
		u.after = timeAfterFunc
	}
	hosts := map[string]bool{}
	for _, base := range []string{cfg.APIBase, cfg.DownloadBase} {
		host, err := checkUpdateBase(base)
		if err != nil {
			return nil, err
		}
		hosts[host] = true
	}
	u.client = newUpdateHTTPClient(cfg.HTTPClient, func(h string) bool { return githubHostAllowed(h) || hosts[strings.ToLower(h)] })

	if v, err := domain.ParseCurrentVersion(cfg.CurrentVersion); err == nil {
		u.current, u.updatable = v, true
	} else if !errors.Is(err, domain.ErrNotUpdatable) {
		u.log.Warn("the app version is not updatable", "version", cfg.CurrentVersion)
	}
	switch key, err := parseUpdatePublicKey(cfg.PublicKey); {
	case err == nil:
		u.pub, u.keyOK = key, true
	case errors.Is(err, ErrUpdateKeyNotConfigured):
		u.log.Warn("the update public key is not configured; updates are checked but never applied")
	default:
		return nil, fmt.Errorf("service: update public key: %w", err)
	}
	u.runCtx, u.runCancel = context.WithCancel(context.Background())
	u.work <- struct{}{}
	return u, nil
}

// checkUpdateBase は起点の URL を検査して、ホスト名を返す。https だけ（loopback は http も可。テスト用）。
func checkUpdateBase(base string) (string, error) {
	pu, err := url.Parse(base)
	if err != nil || pu.Host == "" || pu.RawQuery != "" || pu.User != nil || strings.HasSuffix(base, "/") {
		return "", fmt.Errorf("service: invalid update base URL")
	}
	host := strings.ToLower(pu.Hostname())
	if pu.Scheme != "https" && (pu.Scheme != "http" || !isLoopbackHost(host)) {
		return "", errors.New("service: update base URL must be https")
	}
	return host, nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (u *updater) Start() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.started || u.closed || !u.updatable {
		return
	}
	u.started = true
	u.cleanStaleDirs()
	u.armTimerLocked(u.cfg.StartDelay)
}

// cleanStaleDirs は前回までの検証済みインストーラの一時ディレクトリを消す（起動時。使用中で消せないものは残す）。
func (u *updater) cleanStaleDirs() {
	matches, _ := filepath.Glob(filepath.Join(u.cfg.TempDir, updateTempPrefix+"*"))
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
}

// armTimerLocked は d 後に定期確認を行うタイマーを張る（u.mu を持って呼ぶ）。
func (u *updater) armTimerLocked(d time.Duration) {
	u.timer = u.after(d, u.scheduledCheck)
}

// scheduledCheck は定期確認 1 回。確認のあと、次のタイマーを張る。Close 後は何もしない。
func (u *updater) scheduledCheck() {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.wg.Add(1)
	u.mu.Unlock()
	defer u.wg.Done()

	u.Check(u.runCtx, domain.ActorSystem)

	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.closed {
		u.armTimerLocked(u.cfg.Interval)
	}
}

func (u *updater) Close() {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.closed = true
	for _, stop := range []func() bool{u.timer, u.pollTimer} {
		if stop != nil {
			stop()
		}
	}
	u.mu.Unlock()
	u.runCancel()
	u.wg.Wait()

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.latest != nil && !u.applying {
		u.dropInstallerLocked(u.latest)
	}
}

// dropInstallerLocked は st の検証済みインストーラの一時ディレクトリを消す（u.mu を持って呼ぶ。適用中のものは消さない）。
func (u *updater) dropInstallerLocked(st *candidateState) {
	if st.vi != nil {
		_ = os.RemoveAll(st.vi.dir)
		st.vi = nil
	}
}

func (u *updater) channel(ctx context.Context) domain.Channel {
	ch, err := loadUpdateChannel(ctx, u.cfg.Settings)
	if err != nil {
		u.log.Warn("invalid update channel setting; using stable")
		return domain.ChannelStable
	}
	return ch
}

func (u *updater) applyMode(ctx context.Context) string {
	mode, err := loadUpdateApplyMode(ctx, u.cfg.Settings)
	if err != nil {
		u.log.Warn("invalid update apply mode setting; using confirm")
		return UpdateApplyConfirm
	}
	return mode
}

func (u *updater) Status(ctx context.Context) UpdateStatus {
	ch := u.channel(ctx)
	mode := u.applyMode(ctx)
	last := u.lastCheckedAt(ctx)

	u.mu.Lock()
	defer u.mu.Unlock()
	st := UpdateStatus{
		CurrentVersion: u.cfg.CurrentVersion, Channel: ch, ApplyMode: mode, Updatable: u.updatable, KeyConfigured: u.keyOK,
		LastCheckedAt: last, LastError: u.lastErr, Applying: u.applying,
	}
	// 設定でチャネルを切り替えた直後は、前のチャネルの確認結果を見せない（次の確認まで「新版なし」）。
	if l := u.latest; l != nil && l.channel == ch {
		st.Latest = &UpdateInfo{
			Version: l.cand.Version.String(), Tag: l.cand.Tag, Asset: l.cand.Asset, Notes: l.cand.Notes, PublishedAt: l.cand.PublishedAt,
			Verified: l.vi != nil, RejectReason: l.reject,
		}
	}
	return st
}

// lastCheckedAt は最後に確認した時刻。起動直後は設定（update.last_checked_at）から 1 度だけ読む。
func (u *updater) lastCheckedAt(ctx context.Context) time.Time {
	u.mu.Lock()
	loaded, last := u.loaded, u.lastChecked
	u.mu.Unlock()
	if loaded {
		return last
	}
	var t time.Time
	if raw, ok, err := u.cfg.Settings.Get(ctx, settingUpdateLastCheckedAt); err == nil && ok {
		var ms int64
		if json.Unmarshal(raw, &ms) == nil && ms > 0 {
			t = time.UnixMilli(ms).UTC()
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.loaded {
		u.loaded, u.lastChecked = true, t
	}
	return u.lastChecked
}

func (u *updater) recordChecked(ctx context.Context, now time.Time) {
	u.mu.Lock()
	u.loaded, u.lastChecked = true, now.UTC()
	u.mu.Unlock()
	raw, err := json.Marshal(now.UnixMilli())
	if err == nil {
		err = u.cfg.Settings.Set(ctx, settingUpdateLastCheckedAt, raw)
	}
	if err != nil {
		u.log.Warn("save the last update check time failed", "err", err)
	}
}

// acquireWork は確認・適用の排他を取る。取れなければ ctx の終了まで待つ。
func (u *updater) acquireWork(ctx context.Context) bool {
	select {
	case <-u.work:
		return true
	case <-ctx.Done():
		return false
	}
}

func (u *updater) releaseWork() { u.work <- struct{}{} }

func (u *updater) Check(ctx context.Context, by domain.ActorKind) UpdateStatus {
	if !u.updatable || !u.acquireWork(ctx) {
		return u.Status(ctx)
	}
	func() {
		defer u.releaseWork()
		u.check(ctx, by)
	}()
	return u.Status(ctx)
}

func (u *updater) check(ctx context.Context, by domain.ActorKind) {
	ch := u.channel(ctx)
	cand, err := u.fetchCandidate(ctx, ch)
	u.recordChecked(ctx, u.cfg.Clock.Now())
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		// 非公開のリポジトリ（404・401・403）・オフライン・不正な応答など。URL・ヘッダー・応答の本文は出さない。
		u.log.Warn("update check failed", "code", errorCode(err), "err", err)
		u.setError(errorCode(err))
		return
	}
	u.setError("")

	switch domain.DecideUpdate(ch, u.current, cand.Version) {
	case domain.UpdateAvailable:
		u.consider(ctx, by, ch, cand)
	case domain.UpdateDowngrade:
		u.clearLatest()
		u.log.Warn("the newest release is older than the running version; ignoring", "version", cand.Version.String(), "current", u.cfg.CurrentVersion)
		u.audit(ctx, by, UpdateDowngradeRejectedAuditEvent, auditDenied, UpdateRejectDowngrade, ch, cand)
	default:
		u.clearLatest()
	}
}

func (u *updater) setError(code string) {
	u.mu.Lock()
	u.lastErr = code
	u.mu.Unlock()
}

func (u *updater) clearLatest() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.latest != nil {
		u.dropInstallerLocked(u.latest)
		u.latest = nil
	}
}

// consider は現行より新しい候補 cand を、検証して状態にする。同じ版を検証済みなら取得し直さない。
func (u *updater) consider(ctx context.Context, by domain.ActorKind, ch domain.Channel, cand updateCandidate) {
	u.mu.Lock()
	if l := u.latest; l != nil && l.channel == ch && l.vi != nil && l.cand.Tag == cand.Tag && l.cand.Asset == cand.Asset {
		if _, err := os.Stat(l.vi.path); err == nil {
			l.cand = cand // 注記・公開日時の更新
			u.mu.Unlock()
			u.armPoll()
			return
		}
	}
	u.mu.Unlock()

	if !u.keyOK {
		u.setLatest(&candidateState{cand: cand, channel: ch, reject: UpdateRejectKeyUnset})
		u.log.Warn("a newer version exists, but the update public key is not configured; it will not be applied", "version", cand.Version.String())
		u.audit(ctx, by, UpdateVerifyFailedAuditEvent, auditDenied, UpdateRejectKeyUnset, ch, cand)
		return
	}
	vi, err := u.downloadAndVerify(ctx, cand)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		reason, rejected := rejectionReason(err)
		if !rejected {
			reason = UpdateRejectDownloadFailed
			u.setError(errorCode(err))
		}
		u.setLatest(&candidateState{cand: cand, channel: ch, reject: reason})
		u.log.Warn("the update was not accepted", "version", cand.Version.String(), "reason", reason, "err", scrubError(err))
		u.audit(ctx, by, UpdateVerifyFailedAuditEvent, auditDenied, reason, ch, cand)
		return
	}
	u.setLatest(&candidateState{cand: cand, channel: ch, vi: vi})
	u.audit(ctx, by, UpdateDetectedAuditEvent, auditOK, "", ch, cand)
	u.notify(cand)
	u.armPoll()
}

// setLatest は確認結果を差し替える（古い検証済みインストーラは消す）。
func (u *updater) setLatest(st *candidateState) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.latest != nil {
		u.dropInstallerLocked(u.latest)
	}
	u.latest = st
}

// notify は署名検証に成功した新版を、GUI のトーストで知らせる（同じ版は 1 度だけ）。
func (u *updater) notify(c updateCandidate) {
	u.mu.Lock()
	if u.notifiedTag == c.Tag {
		u.mu.Unlock()
		return
	}
	u.notifiedTag = c.Tag
	u.mu.Unlock()
	if u.cfg.Bus != nil {
		u.cfg.Bus.Publish(Event{Topic: TopicToast, Data: Toast{
			Level: "info", Message: "新しい版 " + c.Version.String() + " があります。「更新」の画面から適用できます。",
		}})
	}
}

// audit は更新の監査を 1 件記録する。同じ（イベント・版・理由）の繰り返しは記録しない。URL・署名・パスは入れない。
func (u *updater) audit(ctx context.Context, by domain.ActorKind, event, result, reason string, ch domain.Channel, c updateCandidate) {
	if u.cfg.Audit == nil {
		return
	}
	key := event + "|" + c.Version.String() + "|" + reason
	if event != UpdateApplyStartedAuditEvent && event != UpdateApplyFailedAuditEvent {
		u.mu.Lock()
		dup := u.auditedKey == key
		u.auditedKey = key
		u.mu.Unlock()
		if dup {
			return
		}
	}
	detail := map[string]any{"version": c.Version.String(), "tag": c.Tag, "current": u.cfg.CurrentVersion, "channel": string(ch)}
	if reason != "" {
		detail["reason"] = reason
	}
	e := repository.AuditEntry{
		TS: u.cfg.Clock.Now(), Kind: updateAuditKind, Event: event, ActorType: by, Result: result, DetailJSON: marshalDetail(detail),
	}
	if result != auditOK {
		e.ErrorCode = reason
	}
	u.cfg.Audit.Record(ctx, e)
}

// busyReasons は適用できない理由を返す（アイドルなら空）。
func (u *updater) busyReasons() []string {
	var out []string
	if _, held := u.cfg.Leases.Holder(); held {
		out = append(out, UpdateBusyLeaseHeld)
	}
	if _, pending := u.cfg.Leases.Pending(); pending {
		out = append(out, UpdateBusyLeasePending)
	}
	if len(u.cfg.Sessions.Sessions()) > 0 {
		out = append(out, UpdateBusySessions)
	}
	if u.cfg.RunningTasks != nil && u.cfg.RunningTasks() > 0 {
		out = append(out, UpdateBusyTasks)
	}
	return out
}

func (u *updater) Apply(ctx context.Context, by domain.ActorKind) error {
	if !u.updatable {
		return ErrUpdateUnavailable
	}
	// 確認・別の適用の途中は待たずに断る（適用は GUI の操作で、確認は最大で数分かかりうる）。
	select {
	case <-u.work:
	default:
		return &UpdateBusyError{Reasons: []string{UpdateBusyApplying}}
	}
	applied := false
	defer func() {
		if !applied {
			u.releaseWork()
		}
	}()

	if !u.keyOK {
		return ErrUpdateKeyNotConfigured
	}
	if reasons := u.busyReasons(); len(reasons) > 0 {
		return &UpdateBusyError{Reasons: reasons}
	}

	ch := u.channel(ctx)
	u.mu.Lock()
	l := u.latest
	if l == nil || l.vi == nil || l.channel != ch {
		u.mu.Unlock()
		return ErrUpdateUnavailable
	}
	cand, vi := l.cand, l.vi
	u.mu.Unlock()

	// 設定の変更・時間の経過を踏まえて、いまの現行の版に対する判定をやり直す。
	if d := domain.DecideUpdate(ch, u.current, cand.Version); d != domain.UpdateAvailable {
		reason := UpdateRejectDowngrade
		if d == domain.UpdateSame {
			reason = "same_version"
		}
		u.audit(ctx, by, UpdateDowngradeRejectedAuditEvent, auditDenied, reason, ch, cand)
		u.clearLatest()
		return ErrUpdateUnavailable
	}
	// 取得から適用までの間にファイルが差し替えられていないことを、起動の直前にもう一度確かめる。
	if err := u.verifyFile(vi.path, vi.sig, cand); err != nil {
		reason, rejected := rejectionReason(err)
		if !rejected {
			reason = "verify_error"
		}
		u.audit(ctx, by, UpdateVerifyFailedAuditEvent, auditDenied, reason, ch, cand)
		u.clearLatest()
		u.log.Warn("the downloaded installer failed verification before applying", "reason", reason)
		return err
	}

	u.audit(ctx, by, UpdateApplyStartedAuditEvent, auditOK, "", ch, cand)
	u.mu.Lock()
	u.applying = true
	u.mu.Unlock()
	if err := u.cfg.Installer.Run(ctx, vi.path); err != nil {
		u.mu.Lock()
		u.applying = false
		u.mu.Unlock()
		u.audit(ctx, by, UpdateApplyFailedAuditEvent, auditError, errorReason(err), ch, cand)
		u.log.Error("start the installer failed", "err", err)
		return fmt.Errorf("start installer: %w", err)
	}
	// インストーラは動き出した。以後、この Updater は確認も適用もしない（work を返さない）。アプリを終える。
	applied = true
	u.log.Info("the installer was started; quitting the app", "version", cand.Version.String())
	if u.cfg.Quit != nil {
		u.cfg.Quit()
	}
	return nil
}

// errorReason は適用の失敗の分類（監査の error_code。OS のエラー文・パスは入れない）。
func errorReason(err error) string {
	switch {
	case errors.Is(err, platform.ErrNotFound):
		return "installer_not_found"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	}
	return "start_failed"
}

// armPoll は auto_idle の適用の試行を、IdlePoll 後に張る（すでに張っていれば何もしない）。
func (u *updater) armPoll() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || u.pollTimer != nil || u.latest == nil || u.latest.vi == nil {
		return
	}
	u.pollTimer = u.after(u.cfg.IdlePoll, u.pollIdle)
}

// pollIdle は検証済みの新版があり、設定が auto_idle で、いまアイドルなら適用する。適用できなければ（接続中など）また張る。
func (u *updater) pollIdle() {
	u.mu.Lock()
	u.pollTimer = nil
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.wg.Add(1)
	u.mu.Unlock()
	defer u.wg.Done()

	ctx := u.runCtx
	if u.applyMode(ctx) == UpdateApplyAutoIdle {
		err := u.Apply(ctx, domain.ActorSystem)
		var busy *UpdateBusyError
		switch {
		case err == nil:
			return
		case !errors.As(err, &busy):
			// 適用の失敗（検証の失敗・起動の失敗）は繰り返さない。次の定期確認が、新版を見つけ直す。
			u.log.Warn("auto-apply of the update failed", "err", err)
			return
		}
	}
	u.armPoll()
}
