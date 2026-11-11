package webtest

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Element は描画した HTML の要素 1 つ（タグ・class・テキスト）。
type Element struct {
	Tag   string
	Class string
	Text  string
}

// Elements は body の中の、tags に挙げたタグの要素を文書順に返す（tags が空なら見出し h1〜h6）。
// テキストは子孫のテキストを連結して空白を詰めたもの。
func Elements(t testing.TB, body string, tags ...string) []Element {
	t.Helper()
	if len(tags) == 0 {
		tags = []string{"h1", "h2", "h3", "h4", "h5", "h6"}
	}
	want := make(map[string]bool, len(tags))
	for _, tag := range tags {
		want[tag] = true
	}
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	var out []Element
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && want[n.Data] {
			var class string
			for _, a := range n.Attr {
				if a.Key == "class" {
					class = a.Val
				}
			}
			out = append(out, Element{Tag: n.Data, Class: class, Text: strings.Join(strings.Fields(text(n)), " ")})
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func text(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(text(c))
	}
	return sb.String()
}
