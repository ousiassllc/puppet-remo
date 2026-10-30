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
