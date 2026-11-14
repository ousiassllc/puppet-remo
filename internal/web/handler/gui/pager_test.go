package gui

import (
	"net/url"
	"reflect"
	"slices"
	"testing"
)

// cursor は次へしか進めないので、前へは back に積んだ道のりで戻る。次へ → 前へで元のページに戻り、1 ページ目には前が無い。
func TestPageTrailWalksForwardAndBack(t *testing.T) {
	first := pageTrail{}
	if first.Page() != 1 {
		t.Errorf("first page = %d", first.Page())
	}
	if _, ok := first.Prev(); ok {
		t.Error("the first page has no previous page")
	}

	second := first.Next("c2")
	third := second.Next("c3")
	if want := (pageTrail{Cursor: "c3", Back: []string{"c2"}}); !reflect.DeepEqual(third, want) {
		t.Fatalf("third = %+v, want %+v", third, want)
	}
	if second.Page() != 2 || third.Page() != 3 {
		t.Errorf("pages = %d, %d", second.Page(), third.Page())
	}
	back2, ok := third.Prev()
	if !ok || back2.Cursor != second.Cursor || !slices.Equal(back2.Back, second.Back) {
		t.Errorf("prev of third = %+v, %v; want %+v", back2, ok, second)
	}
	back1, ok := back2.Prev()
	if !ok || back1.Cursor != "" || back1.Page() != 1 {
		t.Errorf("prev of second = %+v, %v; want the first page", back1, ok)
	}
	// Next は元の道のりを書き換えない（別のページの URL を作っても Back が混ざらない）。
	if len(second.Back) != 0 {
		t.Errorf("second.Back = %v", second.Back)
	}
}

func TestPageURLsKeepFiltersAndEscapeCursors(t *testing.T) {
	prev, next := pageURLs("/x", map[string][]string{"q": {"a b"}}, pageTrail{Cursor: "c 2", Back: nil}, "c+3", true)
	if prev != "/x?q=a+b" {
		t.Errorf("prev = %q", prev)
	}
	if next != "/x?back=c+2&cursor=c%2B3&q=a+b" {
		t.Errorf("next = %q", next)
	}
	if prev, next = pageURLs("/x", nil, pageTrail{}, "", false); prev != "" || next != "" {
		t.Errorf("a single page has no links: %q %q", prev, next)
	}
}

// back の上限（maxPageTrail）の境界: 受け付ける最後のページ（back が上限の件数）から、受け付けない URL（back が上限 + 1）への「次へ」は作らない。
// 押せる「次へ」が 422 にならない。前へはいつでも戻れる。
func TestPageURLsStopAtTheTrailLimit(t *testing.T) {
	trail := func(back int) pageTrail {
		t := pageTrail{Cursor: "c"}
		for range back {
			t.Back = append(t.Back, "b")
		}
		return t
	}
	for _, tc := range []struct {
		back     int
		wantNext bool
	}{{maxPageTrail - 1, true}, {maxPageTrail, false}} {
		tr := trail(tc.back)
		_, next := pageURLs("/x", nil, tr, "n", true)
		if (next != "") != tc.wantNext {
			t.Errorf("back=%d: next = %q, want a link: %v", tc.back, next, tc.wantNext)
		}
		if tc.wantNext {
			// 作った「次へ」の URL は、そのまま受け付けられる（back が上限ちょうど）。
			u, err := url.Parse(next)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(u.Query()["back"]); got != maxPageTrail {
				t.Errorf("next URL has %d back entries, want %d", got, maxPageTrail)
			}
		}
		if prev, _ := pageURLs("/x", nil, tr, "n", true); prev == "" {
			t.Errorf("back=%d: prev must always be available", tc.back)
		}
	}
}
