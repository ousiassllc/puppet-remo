package webtest

import (
	"fmt"
	"strings"
	"testing"
)

// recorder はエラーを記録する testing.TB（検査自身のテスト用）。
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func TestAssertCSPCompliantHTMLDetectsViolations(t *testing.T) {
	bad := map[string]string{
		"inline script":        `<script>alert(1)</script>`,
		"inline style":         `<style>a{}</style>`,
		"style attribute":      `<p style="color:red">x</p>`,
		"event attribute":      `<button onclick="x()">x</button>`,
		"hx-on":                `<div hx-on:click="x()"></div>`,
		"hx-on legacy":         `<div hx-on="click: x()"></div>`,
		"js prefix":            `<div hx-vals="js:{a:1}"></div>`,
		"trigger filter":       `<div hx-trigger="click[ctrlKey]"></div>`,
		"javascript url":       `<a href="javascript:x()">x</a>`,
		"mixed case attribute": `<p STYLE="a:b">x</p>`,
	}
	for name, body := range bad {
		t.Run("detects "+name, func(t *testing.T) {
			r := &recorder{TB: t}
			AssertCSPCompliantHTML(r, body)
			if len(r.errs) == 0 {
				t.Errorf("violation not detected in %s", body)
			}
		})
	}
	ok := `<script src="/static/js/app.js"></script><div hx-headers="{&#34;X-Puppet-Remo&#34;:&#34;1&#34;}" hx-trigger="sse:killswitch"></div><button data-clear="#x">x</button>`
	r := &recorder{TB: t}
	AssertCSPCompliantHTML(r, ok)
	if len(r.errs) != 0 {
		t.Errorf("false positives: %s", strings.Join(r.errs, "; "))
	}
}

func TestAssertHeadingOutline(t *testing.T) {
	ok := map[string]string{
		"h1 h2 h3 h2":       `<h1>a</h1><h2>b</h2><h3>c</h3><h2>d</h2>`,
		"legend is ignored": `<h1>a</h1><fieldset><legend>b</legend></fieldset><h2>c</h2>`,
	}
	for name, body := range ok {
		r := &recorder{TB: t}
		AssertHeadingOutline(r, body)
		if len(r.errs) != 0 {
			t.Errorf("%s: unexpected errors %v", name, r.errs)
		}
	}
	bad := map[string]string{
		"h1 to h3":     `<h1>a</h1><h3>b</h3>`,
		"h3 before h2": `<h1>a</h1><h3>b</h3><h2>c</h2>`,
		"no h1":        `<h2>a</h2>`,
		"two h1":       `<h1>a</h1><h1>b</h1>`,
		"starts at h2": `<h2>a</h2><h1>b</h1>`,
	}
	for name, body := range bad {
		r := &recorder{TB: t}
		AssertHeadingOutline(r, body)
		if len(r.errs) == 0 {
			t.Errorf("%s: not detected in %s", name, body)
		}
	}
}
