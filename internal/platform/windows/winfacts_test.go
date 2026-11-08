package windows

import (
	"slices"
	"strconv"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestDecodeStyle(t *testing.T) {
	tests := []struct {
		name      string
		style, ex uintptr
		want      domain.WindowStyle
	}{
		{"none", 0, 0, domain.WindowStyle{}},
		{"popup", wsPopup, 0, domain.WindowStyle{Popup: true}},
		{"child", wsChild, 0, domain.WindowStyle{Child: true}},
		{"tool", 0, wsExToolWindow, domain.WindowStyle{Tool: true}},
		{"transparent", 0, wsExTransparent, domain.WindowStyle{Transparent: true}},
		{"noactivate", 0, wsExNoActivate, domain.WindowStyle{NoActivate: true}},
		{"menu-like popup tool noactivate", wsPopup | 0x00800000, wsExToolWindow | wsExNoActivate | 0x00080000, domain.WindowStyle{Popup: true, Tool: true, NoActivate: true}},
		{"unrelated bits only", 0x00C00000, 0x00000100, domain.WindowStyle{}},
	}
	for _, tt := range tests {
		if got := decodeStyle(tt.style, tt.ex); got != tt.want {
			t.Errorf("%s: decodeStyle(%#x, %#x) = %+v, want %+v", tt.name, tt.style, tt.ex, got, tt.want)
		}
	}
}

func TestFiletimeValue(t *testing.T) {
	if got, want := filetimeValue(0x01DC0000, 0x89ABCDEF), uint64(0x01DC000089ABCDEF); got != want {
		t.Errorf("filetimeValue = %#x, want %#x", got, want)
	}
	if filetimeValue(0, 0) != 0 {
		t.Error("a zero FILETIME must stay 0 (unknown)")
	}
}

// owners は hwnd → 所有者の表。0 は所有者なし。
func nextOf(owners map[uintptr]uintptr) func(uintptr) uintptr {
	return func(h uintptr) uintptr { return owners[h] }
}

func factOf(missing ...uintptr) func(uintptr) (domain.WindowLink, bool) {
	return func(h uintptr) (domain.WindowLink, bool) {
		if slices.Contains(missing, h) {
			return domain.WindowLink{}, false
		}
		return domain.WindowLink{ID: strconv.Itoa(int(h)), PID: 10, ProcessStart: 5, Class: "C"}, true
	}
}

func ids(chain []domain.WindowLink) []string {
	var out []string
	for _, l := range chain {
		if l.Unverifiable {
			out = append(out, "!")
			continue
		}
		out = append(out, l.ID)
	}
	return out
}

func TestWalkLinks(t *testing.T) {
	long := map[uintptr]uintptr{}
	for h := uintptr(1); h <= 20; h++ {
		long[h] = h + 1
	}
	exactly8 := map[uintptr]uintptr{}
	for h := uintptr(1); h < 8; h++ {
		exactly8[h] = h + 1
	}
	tests := []struct {
		name    string
		first   uintptr
		self    uintptr
		owners  map[uintptr]uintptr
		missing []uintptr
		want    []string
	}{
		{"no owner", 0, 100, nil, nil, nil},
		{"one hop", 1, 100, map[uintptr]uintptr{1: 0}, nil, []string{"1"}},
		{"two hops", 1, 100, map[uintptr]uintptr{1: 2, 2: 0}, nil, []string{"1", "2"}},
		{"exactly 8 hops is complete (no marker)", 1, 100, exactly8, nil, []string{"1", "2", "3", "4", "5", "6", "7", "8"}},
		{"more than 8 hops ends with a marker", 1, 100, long, nil, []string{"1", "2", "3", "4", "5", "6", "7", "8", "!"}},
		{"cycle among owners", 1, 100, map[uintptr]uintptr{1: 2, 2: 3, 3: 1}, nil, []string{"1", "2", "3", "!"}},
		{"owner is the window itself", 1, 100, map[uintptr]uintptr{1: 100}, nil, []string{"1", "!"}},
		{"first is the window itself", 100, 100, nil, nil, []string{"!"}},
		{"owner vanished while walking", 1, 100, map[uintptr]uintptr{1: 2, 2: 3, 3: 0}, []uintptr{2}, []string{"1", "!"}},
		{"first owner unreadable", 1, 100, nil, []uintptr{1}, []string{"!"}},
	}
	for _, tt := range tests {
		got := ids(walkLinks(tt.first, tt.self, nextOf(tt.owners), factOf(tt.missing...)))
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s: chain = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// walkLinks が作る鎖は、domain.ResolveGroup が 8 段で T に至るもの・9 段以上のものを正しく扱える形になっている。
func TestWalkLinksFeedsResolveGroup(t *testing.T) {
	const tID = 50
	tw := domain.WindowInfo{ID: strconv.Itoa(tID), PID: 10, ProcessStart: 5, Class: "C"}
	chainOf := func(hops int) []domain.WindowLink {
		owners := map[uintptr]uintptr{}
		for h := 1; h < hops; h++ {
			owners[uintptr(h)] = uintptr(h + 1)
		}
		owners[uintptr(hops)] = tID // hops 段目が T
		f := func(h uintptr) (domain.WindowLink, bool) {
			return domain.WindowLink{ID: strconv.Itoa(int(h)), PID: 10, ProcessStart: 5, Class: "C"}, true
		}
		return walkLinks(1, 100, nextOf(owners), f)
	}
	for hops, want := range map[int]string{1: "owned", 7: "owned", 8: "unverifiable"} {
		w := domain.WindowInfo{ID: "100", PID: 10, ProcessStart: 5, Class: "D", OwnerChain: chainOf(hops)}
		g := domain.ResolveGroup(domain.RefOf(tw), []domain.WindowInfo{w, tw})
		_, member := g.Member("100")
		got := "unverifiable"
		if member {
			got = "owned"
		}
		if got != want {
			t.Errorf("T at hop %d (counting the window's own owner as hop 1; the chain has %d entries): %s, want %s", hops, len(w.OwnerChain), got, want)
		}
	}
}
