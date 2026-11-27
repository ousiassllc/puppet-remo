package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

func sampleTokenRecord(n int) chatgptTokenRecord {
	return chatgptTokenRecord{
		AccessToken: strings.Repeat("a", n), RefreshToken: "rt-" + strings.Repeat("r", 200), IDToken: strings.Repeat("i", n),
		ExpiresAt: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC), Scope: testChatGPTScope,
	}
}

func TestChatGPTTokenStoreRoundTrip(t *testing.T) {
	for _, n := range []int{100, 1500, 4000} { // 1 part・2 parts・3 parts 以上（2560 超え）
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			secrets := platformtest.NewFakeSecretStore()
			st := chatgptTokenStore{secrets: secrets}
			rec := sampleTokenRecord(n)
			if err := st.save(rec); err != nil {
				t.Fatal(err)
			}
			got, found, err := st.load()
			if err != nil || !found || got != rec {
				t.Fatalf("load = found %v err %v equal %v", found, err, got == rec)
			}
			// どの 1 件も 2000 バイト以下（資格情報マネージャーの 2560 バイトに収まる）。
			for i := 1; ; i++ {
				v, err := secrets.Get(chatgptPartName(i))
				if err != nil {
					break
				}
				if len(v) > 2000 {
					t.Errorf("part %d is %d bytes", i, len(v))
				}
			}
			if h, _ := secrets.Get(platform.SecretChatGPT); !strings.HasPrefix(h, `{"v":1,"parts":`) {
				t.Errorf("header = %q", h)
			}
		})
	}
}

func TestChatGPTTokenStoreOver2560Bytes(t *testing.T) {
	secrets := platformtest.NewFakeSecretStore()
	st := chatgptTokenStore{secrets: secrets}
	rec := sampleTokenRecord(3000) // 本体は 6000 バイトを越える
	if err := st.save(rec); err != nil {
		t.Fatal(err)
	}
	total, parts := 0, 0
	for i := 1; ; i++ {
		v, err := secrets.Get(chatgptPartName(i))
		if err != nil {
			break
		}
		parts++
		total += len(v)
		if len(v) > 2000 {
			t.Errorf("part %d is %d bytes", i, len(v))
		}
	}
	if total <= 2560 || parts < 3 {
		t.Fatalf("total %d bytes in %d parts; the test must cover a record over 2560 bytes", total, parts)
	}
	if got, found, _ := st.load(); !found || got != rec {
		t.Fatal("the record did not round-trip")
	}
}

func TestChatGPTTokenStoreMultibyteIsNotCut(t *testing.T) {
	secrets := platformtest.NewFakeSecretStore()
	st := chatgptTokenStore{secrets: secrets}
	rec := sampleTokenRecord(10)
	rec.Scope = strings.Repeat("あ", 1500) // 4500 バイトの日本語。文字の途中で切らない
	if err := st.save(rec); err != nil {
		t.Fatal(err)
	}
	if got, found, _ := st.load(); !found || got != rec {
		t.Fatal("multibyte record did not round-trip")
	}
}

func TestChatGPTTokenStoreShrinkingCleansOldParts(t *testing.T) {
	secrets := platformtest.NewFakeSecretStore()
	st := chatgptTokenStore{secrets: secrets}
	if err := st.save(sampleTokenRecord(4000)); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(chatgptPartName(3)); err != nil {
		t.Fatalf("setup: part 3 should exist: %v", err)
	}
	small := sampleTokenRecord(100)
	if err := st.save(small); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(chatgptPartName(2)); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("old part 2 remains: %v", err)
	}
	if _, err := secrets.Get(chatgptPartName(3)); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("old part 3 remains: %v", err)
	}
	if got, found, _ := st.load(); !found || got != small {
		t.Fatal("the small record did not load")
	}
}

func TestChatGPTTokenStoreBrokenRecordIsAbsent(t *testing.T) {
	setup := func(t *testing.T) (*platformtest.FakeSecretStore, chatgptTokenStore) {
		secrets := platformtest.NewFakeSecretStore()
		st := chatgptTokenStore{secrets: secrets}
		if err := st.save(sampleTokenRecord(4000)); err != nil {
			t.Fatal(err)
		}
		return secrets, st
	}
	tests := map[string]func(t *testing.T, s *platformtest.FakeSecretStore){
		"part が欠けている": func(t *testing.T, s *platformtest.FakeSecretStore) { _ = s.Delete(chatgptPartName(2)) },
		"見出しの parts が多い": func(t *testing.T, s *platformtest.FakeSecretStore) {
			_ = s.Set(platform.SecretChatGPT, `{"v":1,"parts":9}`)
		},
		"見出しの parts が少ない（JSON が壊れる）": func(t *testing.T, s *platformtest.FakeSecretStore) {
			_ = s.Set(platform.SecretChatGPT, `{"v":1,"parts":1}`)
		},
		"版が違う": func(t *testing.T, s *platformtest.FakeSecretStore) {
			_ = s.Set(platform.SecretChatGPT, `{"v":2,"parts":3}`)
		},
		"見出しが JSON でない": func(t *testing.T, s *platformtest.FakeSecretStore) { _ = s.Set(platform.SecretChatGPT, `garbage`) },
		"parts が 0": func(t *testing.T, s *platformtest.FakeSecretStore) {
			_ = s.Set(platform.SecretChatGPT, `{"v":1,"parts":0}`)
		},
		"見出しだけ消えた": func(t *testing.T, s *platformtest.FakeSecretStore) { _ = s.Delete(platform.SecretChatGPT) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			secrets, st := setup(t)
			mutate(t, secrets)
			if _, found, err := st.load(); found || err != nil {
				t.Fatalf("load = found %v, err %v; want absent without an error", found, err)
			}
		})
	}
}

func TestChatGPTTokenStoreDeleteAndReadErrors(t *testing.T) {
	secrets := platformtest.NewFakeSecretStore()
	st := chatgptTokenStore{secrets: secrets}
	if err := st.delete(); err != nil { // 何も無くてもエラーにしない
		t.Fatal(err)
	}
	if err := st.save(sampleTokenRecord(4000)); err != nil {
		t.Fatal(err)
	}
	if err := st.delete(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{platform.SecretChatGPT, chatgptPartName(1), chatgptPartName(2), chatgptPartName(3)} {
		if _, err := secrets.Get(name); !errors.Is(err, platform.ErrNotFound) {
			t.Errorf("%s remains after delete", name)
		}
	}
	boom := errors.New("vault unavailable")
	secrets.SetError("Get", boom)
	if _, _, err := st.load(); !errors.Is(err, boom) {
		t.Errorf("load with a failing vault = %v, want the error", err)
	}
}

func TestChatGPTStartupWithTokensIsConnectedWithoutNetwork(t *testing.T) {
	e := newChatGPTEnv(t, chatgptEnvOpts{seed: true})
	if st := e.svc.Status(); st.State != PlanConnected || st.AccountEmail != testChatGPTEmail {
		t.Fatalf("Status = %+v", st)
	}
	if _, ok := e.svc.Ready(); !ok {
		t.Error("Ready = false")
	}
	ex, rf, rv := e.fake.snapshot()
	if len(ex)+len(rf)+len(rv) != 0 || len(e.fake.apiAuth) != 0 {
		t.Error("the network was used at startup")
	}
	empty := newChatGPTEnv(t, chatgptEnvOpts{})
	if st := empty.svc.Status(); st.State != PlanSignedOut || st.AccountEmail != "" {
		t.Fatalf("Status without tokens = %+v", st)
	}
	// 壊れた記録は「トークンなし」。
	_ = empty.secrets.Set(platform.SecretChatGPT, "garbage")
	empty.restart()
	if empty.svc.Status().State != PlanSignedOut {
		t.Error("a broken record counted as connected")
	}
}
