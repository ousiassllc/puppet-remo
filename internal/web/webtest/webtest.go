// Package webtest は Templ とハンドラのテストが共有する検査。本番のバイナリには含まれない。
package webtest

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"golang.org/x/net/html"
)

// Render は comp を HTML 文字列にする。
func Render(t testing.TB, comp templ.Component) string {
	t.Helper()
	var sb strings.Builder
	if err := comp.Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

// AssertCSPCompliantHTML は docs/architecture/frontend.md §7.1・§9 の規約を検査する。描画した HTML に次のものが無いこと:
// src の無い <script>、<style>、style= 属性、on* 属性、hx-on 系の属性、hx-* の js: 接頭辞、hx-trigger の [...] フィルター、
// javascript: の URL。
func AssertCSPCompliantHTML(t testing.TB, body string) {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(body))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		switch tok.Data {
		case "style":
			t.Errorf("inline <style> element: %s", tok)
		case "script":
			if attr(tok, "src") == "" {
				t.Errorf("inline <script>: %s", tok)
			}
		}
		for _, a := range tok.Attr {
			k := strings.ToLower(a.Key)
			switch {
			case k == "style":
				t.Errorf("style attribute on <%s>: %s", tok.Data, a.Val)
			case strings.HasPrefix(k, "on"):
				t.Errorf("event handler attribute %q on <%s>", a.Key, tok.Data)
			case strings.HasPrefix(k, "hx-on") || strings.HasPrefix(k, "data-hx-on"):
				t.Errorf("hx-on attribute %q on <%s>", a.Key, tok.Data)
			}
			if strings.HasPrefix(k, "hx-") && strings.Contains(a.Val, "js:") {
				t.Errorf("js: prefix in %s=%q", a.Key, a.Val)
			}
			if k == "hx-trigger" && strings.ContainsAny(a.Val, "[]") {
				t.Errorf("hx-trigger filter: %q", a.Val)
			}
			if (k == "href" || k == "src" || k == "action" || k == "formaction") &&
				strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Val)), "javascript:") {
				t.Errorf("javascript: URL in %s", a.Key)
			}
		}
	}
}

func attr(tok html.Token, key string) string {
	for _, a := range tok.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
