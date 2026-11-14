package gui

import (
	"net/url"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxPageTrail は「戻る」の道のりに受け付ける長さの上限。道のりは URL に載るので、利用者が際限なく長い列を付けられないようにする。
const maxPageTrail = 100

// pageTrail は cursor（直前のページの最後の id）でページを送る一覧（監査ログ・タスク）の「前へ」を作るための、URL に載せる道のり。
// cursor は次へしか進めないので、今のページの先頭の cursor（Cursor。空なら 1 ページ目）と、そこまでに通った 2 ページ目以降の先頭の cursor（Back。
// 古い順）を URL（`cursor=`・繰り返しの `back=`）に持ち、前のページは Back の末尾を cursor に戻して作る。ページ番号は道のりの長さから決まる。
type pageTrail struct {
	Cursor string
	Back   []string
}

// parsePageTrail は GET の cursor と back を読む。back が cursor なしにある、空の項目がある、長すぎるときは ok = false。
func parsePageTrail(c *gin.Context) (t pageTrail, ok bool) {
	t.Cursor = strings.TrimSpace(c.Query("cursor"))
	for _, b := range c.QueryArray("back") {
		t.Back = append(t.Back, strings.TrimSpace(b))
	}
	if t.Cursor == "" && len(t.Back) > 0 || len(t.Back) > maxPageTrail || slices.Contains(t.Back, "") {
		return pageTrail{}, false
	}
	return t, true
}

// Page は N ページ目の N（cursor なしが 1。cursor があれば Back の長さ + 2）。
func (t pageTrail) Page() int {
	if t.Cursor == "" {
		return 1
	}
	return len(t.Back) + 2
}

// Next は、今のページの次（cursor = next）のページの道のり。
func (t pageTrail) Next(next string) pageTrail {
	back := slices.Clone(t.Back)
	if t.Cursor != "" {
		back = append(back, t.Cursor)
	}
	return pageTrail{Cursor: next, Back: back}
}

// Prev は前のページの道のり。1 ページ目には前が無いので ok = false。
func (t pageTrail) Prev() (pageTrail, bool) {
	switch {
	case t.Cursor == "":
		return pageTrail{}, false
	case len(t.Back) == 0:
		return pageTrail{}, true
	}
	last := len(t.Back) - 1
	return pageTrail{Cursor: t.Back[last], Back: slices.Clone(t.Back[:last])}, true
}

// apply は道のりを q の cursor・back に書く（1 ページ目は何も付けない）。
func (t pageTrail) apply(q url.Values) {
	if t.Cursor == "" {
		return
	}
	q.Set("cursor", t.Cursor)
	for _, b := range t.Back {
		q.Add("back", b)
	}
}

// pageURLs は base に filters（絞り込み）と道のりを付けた「前へ」「次へ」の URL。hasNext が false なら次は空、1 ページ目なら前は空。
func pageURLs(base string, filters url.Values, t pageTrail, next string, hasNext bool) (prev, nextURL string) {
	build := func(t pageTrail) string {
		q := url.Values{}
		for k, v := range filters {
			q[k] = v
		}
		t.apply(q)
		if len(q) == 0 {
			return base
		}
		return base + "?" + q.Encode()
	}
	if p, ok := t.Prev(); ok {
		prev = build(p)
	}
	if hasNext {
		nextURL = build(t.Next(next))
	}
	return prev, nextURL
}
