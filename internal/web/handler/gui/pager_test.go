package gui

import (
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
