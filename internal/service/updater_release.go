package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは GitHub Releases の確認（FR-H09）。認証なしで HTTPS の REST API を呼び（リポジトリは公開が前提）、
// 送信先は GitHub だけにする。ダウンロードの URL は応答の値を信じず、タグとファイル名から自分で組み立てる。

// 更新の取得元と上限。
const (
	// DefaultUpdateRepo は更新を取得するリポジトリ（owner/name）。
	DefaultUpdateRepo = "ousiassllc/puppet-remo"
	// DefaultUpdateAPIBase・DefaultUpdateDownloadBase は GitHub の API と Release のダウンロードの起点（https のみ）。
	DefaultUpdateAPIBase      = "https://api.github.com"
	DefaultUpdateDownloadBase = "https://github.com"

	maxReleaseJSONBytes   = 4 << 20   // Release の一覧の応答の上限
	maxInstallerBytes     = 300 << 20 // インストーラの上限（300 MiB）
	maxSignatureBytes     = 4 << 10   // .minisig の上限（4 KiB）
	maxReleaseNotesRunes  = 1000      // GUI に出すリリースノートの要約の長さ
	releaseListPerPage    = 30        // nightly チャネルで見る Release の件数
	apiRequestTimeout     = 30 * time.Second
	downloadTimeout       = 15 * time.Minute
	maxUpdateRedirects    = 5
	updateTrustedCommentF = "version=%s file=%s" // 署名の trusted comment（版とファイル名を署名に結ぶ）
)

// UpdateTrustedComment は、署名の trusted comment に入れる文字列（版とインストーラのファイル名。リリースの署名ツールと検証側が同じ形を使う）。
// 署名済みの古いインストーラを新しい版に付け替える（ロールバック）攻撃を防ぐため、検証側は Release の版・ファイル名と一致を確かめる。
func UpdateTrustedComment(version, file string) string {
	return fmt.Sprintf(updateTrustedCommentF, version, file)
}

// 確認の失敗の分類（UpdateStatus.ErrorCode）。URL・ヘッダー・応答の本文は含めない。
const (
	UpdateErrNetwork     = "network"      // 通信エラー・タイムアウト
	UpdateErrNotFound    = "http_404"     // Release が無い（リポジトリが非公開の場合を含む）
	UpdateErrForbidden   = "http_403"     // 認証が要る・レート制限（非公開のリポジトリを含む）
	UpdateErrUnauthorize = "http_401"     // 同上
	UpdateErrHTTP        = "http_error"   // その他のステータス
	UpdateErrBadResponse = "bad_response" // 不正な JSON・想定外の形
	UpdateErrTooLarge    = "too_large"    // 応答・インストーラが上限を超える
	UpdateErrNoInstaller = "no_installer" // Release にインストーラが無い
)

// fetchError は GitHub への通信の失敗。code は UpdateErr*。err は URL を含まない原因（ログ用）。
type fetchError struct {
	code string
	err  error
}

func (e *fetchError) Error() string {
	if e.err == nil {
		return "update fetch failed: " + e.code
	}
	return "update fetch failed: " + e.code + ": " + e.err.Error()
}
func (e *fetchError) Unwrap() error { return e.err }

func errorCode(err error) string {
	var fe *fetchError
	if errors.As(err, &fe) {
		return fe.code
	}
	return UpdateErrNetwork
}

// scrubError は err から URL（クエリに署名つきのトークンを含みうる）を取り除いた原因だけを返す。
func scrubError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, scrubError(ue.Err))
	}
	return err
}

// githubRelease は Releases API の応答のうち使う項目。
type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Body        string        `json:"body"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// updateCandidate は Release から選んだ更新の候補。
type updateCandidate struct {
	Version     domain.Version
	Tag         string
	Asset       string // インストーラのファイル名
	AssetSize   int64
	HasSig      bool // <Asset>.minisig が添付されている
	Notes       string
	PublishedAt time.Time
}

// candidateFrom は r から候補を作る。ドラフト・インストーラの無い Release・タグと版が合わない Release は ok = false。
func candidateFrom(r githubRelease) (updateCandidate, bool) {
	if r.Draft {
		return updateCandidate{}, false
	}
	for _, a := range r.Assets {
		v, err := domain.ParseInstallerAsset(a.Name)
		if err != nil || v.ReleaseTag() != r.TagName {
			continue
		}
		c := updateCandidate{
			Version: v, Tag: r.TagName, Asset: a.Name, AssetSize: a.Size,
			Notes: summarizeNotes(r.Body), PublishedAt: r.PublishedAt,
		}
		for _, s := range r.Assets {
			if s.Name == a.Name+".minisig" {
				c.HasSig = true
			}
		}
		return c, true
	}
	return updateCandidate{}, false
}

// newestCandidate は rs の中で最も新しい候補を返す（stable は prerelease を、ナイトリーの版を除く）。無ければ ok = false。
func newestCandidate(rs []githubRelease, ch domain.Channel) (updateCandidate, bool) {
	var best updateCandidate
	found := false
	for _, r := range rs {
		c, ok := candidateFrom(r)
		if !ok || (ch != domain.ChannelNightly && (r.Prerelease || c.Version.Nightly)) {
			continue
		}
		if !found || domain.CompareVersions(c.Version, best.Version) > 0 {
			best, found = c, true
		}
	}
	return best, found
}

// summarizeNotes はリリースノートを、制御文字を除いて maxReleaseNotesRunes 文字までにする（外部由来の文字列。表示側が HTML エスケープする）。
func summarizeNotes(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == '\r' {
			continue
		}
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		if n == maxReleaseNotesRunes {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// fetchCandidate は channel の最新の候補を取得する。stable は /releases/latest（プレリリースを含まない最新の正式版）、
// nightly は直近の Release の一覧から、プレリリースと正式版のうち最も新しい版を選ぶ。
func (u *updater) fetchCandidate(ctx context.Context, ch domain.Channel) (updateCandidate, error) {
	var rels []githubRelease
	if ch == domain.ChannelNightly {
		if err := u.getJSON(ctx, "/repos/"+u.cfg.Repo+"/releases?per_page="+strconv.Itoa(releaseListPerPage), &rels); err != nil {
			return updateCandidate{}, err
		}
	} else {
		var r githubRelease
		if err := u.getJSON(ctx, "/repos/"+u.cfg.Repo+"/releases/latest", &r); err != nil {
			return updateCandidate{}, err
		}
		rels = []githubRelease{r}
	}
	c, ok := newestCandidate(rels, ch)
	if !ok {
		return updateCandidate{}, &fetchError{code: UpdateErrNoInstaller}
	}
	return c, nil
}

// getJSON は API の path（/ から）を GET して out へ読む。認証ヘッダーは付けない。
func (u *updater) getJSON(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, apiRequestTimeout)
	defer cancel()
	resp, err := u.get(ctx, u.cfg.APIBase+path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseJSONBytes+1))
	if err != nil {
		return &fetchError{code: UpdateErrNetwork, err: scrubError(err)}
	}
	if len(body) > maxReleaseJSONBytes {
		return &fetchError{code: UpdateErrTooLarge}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &fetchError{code: UpdateErrBadResponse}
	}
	return nil
}

// get は rawURL を GET する。2xx 以外は *fetchError（本文は読まない）。呼び出し側が Body を閉じる。
func (u *updater) get(ctx context.Context, rawURL, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &fetchError{code: UpdateErrNetwork, err: scrubError(err)}
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "puppet-remo/"+u.cfg.CurrentVersion)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, &fetchError{code: UpdateErrNetwork, err: scrubError(err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = resp.Body.Close()
		return nil, &fetchError{code: statusCode(resp.StatusCode), err: fmt.Errorf("status %d", resp.StatusCode)}
	}
	return resp, nil
}

func statusCode(status int) string {
	switch status {
	case http.StatusNotFound:
		return UpdateErrNotFound
	case http.StatusForbidden:
		return UpdateErrForbidden
	case http.StatusUnauthorized:
		return UpdateErrUnauthorize
	}
	return UpdateErrHTTP
}

// newUpdateHTTPClient は base を元に、リダイレクトの先を https かつ許可したホストだけにするクライアントを作る
// （base は変更しない）。
func newUpdateHTTPClient(base *http.Client, hostAllowed func(string) bool) *http.Client {
	c := &http.Client{}
	if base != nil {
		*c = *base
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxUpdateRedirects {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" || !hostAllowed(req.URL.Hostname()) {
			return errors.New("redirect to a disallowed destination")
		}
		return nil
	}
	return c
}

// githubHostAllowed は既定のリダイレクト先の許可（GitHub の API・Release の配信元だけ）。
func githubHostAllowed(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "api.github.com" || host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}
