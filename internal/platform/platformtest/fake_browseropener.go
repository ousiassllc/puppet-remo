package platformtest

import (
	"context"
	"net/url"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.BrowserOpener = (*FakeBrowserOpener)(nil)

// FakeBrowserOpener は platform.BrowserOpener の偽の実装。ブラウザは起動せず、開いた URL を Opened に記録する。
// 実装と同じく https の URL だけを受け付ける。テストは Next で、開かれるのを（固定時間の待ちなしに）待って URL を取り出せる。
// 開いた URL にはサインインの state などが入るので、テストの出力に出さないこと。
type FakeBrowserOpener struct {
	Recorder

	mu     sync.Mutex
	opened []string
	ch     chan string
	http   bool
}

func NewFakeBrowserOpener() *FakeBrowserOpener {
	return &FakeBrowserOpener{ch: make(chan string, 64)}
}

// AllowHTTP は http の URL も開けることにする。偽の認可サーバー（httptest。http://127.0.0.1:…）へ向けるテスト用で、
// 実装の https だけという性質は契約テスト（既定の偽物）が検査する。
func (f *FakeBrowserOpener) AllowHTTP() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.http = true
}

// Opened は開いた URL を、開いた順に返す。
func (f *FakeBrowserOpener) Opened() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...)
}

// Next は、まだ Next で受け取っていない次の URL が開かれるまで待って返す。ctx が終わったら ctx.Err()。
func (f *FakeBrowserOpener) Next(ctx context.Context) (string, error) {
	select {
	case u := <-f.ch:
		return u, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *FakeBrowserOpener) OpenURL(ctx context.Context, rawURL string) error {
	// 記録の引数に URL は残さない（秘密を含みうる）。
	if err := f.enter(ctx, "OpenURL"); err != nil {
		return err
	}
	for _, r := range rawURL {
		if r <= ' ' || r == 0x7f {
			return domain.ErrInvalidArgument.WithMsg("browser url contains a control or space character")
		}
	}
	f.mu.Lock()
	allowHTTP := f.http
	f.mu.Unlock()
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || !(strings.EqualFold(u.Scheme, "https") || (allowHTTP && strings.EqualFold(u.Scheme, "http"))) {
		return domain.ErrInvalidArgument.WithMsg("browser url must be an https url")
	}
	f.mu.Lock()
	f.opened = append(f.opened, rawURL)
	f.mu.Unlock()
	select {
	case f.ch <- rawURL:
	default: // 受け取られないまま 64 件を超えた分は Next に流さない（Opened には残る）
	}
	return nil
}
