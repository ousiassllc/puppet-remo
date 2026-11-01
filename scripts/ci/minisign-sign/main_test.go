package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aead.dev/minisign"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/scrypt"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このテストは、署名ツールの出力が、アプリの検証側（service.Updater）と同じ形で読めることを、テスト専用に生成した鍵で確かめる。

const (
	testVersion = "0.1.1"
	testAsset   = "puppet-remo-0.1.1-amd64-installer.exe"
)

type keyFiles struct {
	dir     string
	pubText []byte
	plain   string // パスワードなしの秘密鍵のファイル
	enc     string // パスワード付き
	pass    string
}

var (
	sharedKeysOnce sync.Once
	sharedKeys     keyFiles
)

// newKeyFiles はテスト専用の鍵を 1 度だけ作り、全テストで共有する。鍵のファイルを置くディレクトリは TestMain が消す。
func newKeyFiles(t *testing.T) keyFiles {
	t.Helper()
	sharedKeysOnce.Do(func() { sharedKeys = makeKeyFiles(t) })
	return sharedKeys
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedKeys.dir != "" {
		_ = os.RemoveAll(sharedKeys.dir)
	}
	os.Exit(code)
}

func makeKeyFiles(t *testing.T) keyFiles {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubText, _ := pub.MarshalText()
	dir, err := os.MkdirTemp("", "minisign-sign-test-")
	if err != nil {
		t.Fatal(err)
	}
	plainText, err := priv.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	pass := "test-only-" + strings.Repeat("p", 8)
	encText := cheapEncryptedKey(t, pass, priv)
	k := keyFiles{dir: dir, pubText: pubText, plain: filepath.Join(dir, "plain.key"), enc: filepath.Join(dir, "enc.key"), pass: pass}
	for path, b := range map[string][]byte{k.plain: plainText, k.enc: encText} {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return k
}

func writeInstaller(t *testing.T, dir string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, testAsset)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// verify は署名を、アプリの検証と同じ手順（事前ハッシュ・trusted comment の照合）で確かめる。
func verify(t *testing.T, pubText, data, sigText []byte, version, file string) bool {
	t.Helper()
	var pub minisign.PublicKey
	if err := pub.UnmarshalText(pubText); err != nil {
		t.Fatal(err)
	}
	var sig minisign.Signature
	if err := sig.UnmarshalText(sigText); err != nil {
		return false
	}
	r := minisign.NewReader(bytes.NewReader(data))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	return r.Verify(pub, sigText) && sig.TrustedComment == service.UpdateTrustedComment(version, file)
}

func TestSignRoundTrip(t *testing.T) {
	k := newKeyFiles(t)
	data := []byte("installer " + strings.Repeat("0123456789", 1000))
	for name, tc := range map[string]struct{ key, password string }{
		"no password": {k.plain, ""},
		"password":    {k.enc, k.pass},
	} {
		t.Run(name, func(t *testing.T) {
			in := writeInstaller(t, t.TempDir(), data)
			var stdout, stderr bytes.Buffer
			if rc := run([]string{"-key", tc.key, "-version", testVersion, "-in", in}, env(map[string]string{"MINISIGN_PASSWORD": tc.password}), &stdout, &stderr); rc != 0 {
				t.Fatalf("rc = %d, stderr = %s", rc, stderr.String())
			}
			sig, err := os.ReadFile(in + ".minisig")
			if err != nil {
				t.Fatal(err)
			}
			if !verify(t, k.pubText, data, sig, testVersion, testAsset) {
				t.Fatal("the app's verification rejects the signature the tool made")
			}
			// 改ざん・版の付け替え・別のファイル名は、検証に通らない。
			tampered := bytes.Clone(data)
			tampered[10] ^= 1
			if verify(t, k.pubText, tampered, sig, testVersion, testAsset) {
				t.Error("a tampered installer verified")
			}
			if verify(t, k.pubText, data, sig, "0.1.2", testAsset) || verify(t, k.pubText, data, sig, testVersion, "other.exe") {
				t.Error("the signature is not bound to the version and file name")
			}
			if out := stdout.String(); strings.Contains(out, tc.password) && tc.password != "" || strings.Contains(out, tc.key) {
				t.Errorf("stdout leaks a secret or key path: %q", out)
			}
		})
	}
}

func TestSignWithOutPathAndNightlyVersion(t *testing.T) {
	k := newKeyFiles(t)
	data := []byte("nightly installer")
	dir := t.TempDir()
	const asset = "puppet-remo-0.1.1-nightly.20261102.abc1234-amd64-installer.exe"
	const version = "0.1.1-nightly.20261102+abc1234"
	in := filepath.Join(dir, asset)
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "custom.minisig")
	var stdout, stderr bytes.Buffer
	if rc := run([]string{"-key", k.plain, "-version", version, "-in", in, "-out", out}, env(nil), &stdout, &stderr); rc != 0 {
		t.Fatalf("rc = %d: %s", rc, stderr.String())
	}
	sig, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !verify(t, k.pubText, data, sig, version, asset) {
		t.Error("nightly signature does not verify")
	}
}

func TestSignFailures(t *testing.T) {
	k := newKeyFiles(t)
	in := writeInstaller(t, t.TempDir(), []byte("data"))
	empty := writeInstaller(t, t.TempDir(), nil)
	secretWord := k.pass
	const wrongWord = "not-the-password-xyz"
	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"no arguments", nil, nil},
		{"missing version", []string{"-key", k.plain, "-in", in}, nil},
		{"unknown flag", []string{"-key", k.plain, "-version", testVersion, "-in", in, "-x"}, nil},
		{"extra argument", []string{"-key", k.plain, "-version", testVersion, "-in", in, "extra"}, nil},
		{"missing key file", []string{"-key", filepath.Join(k.dir, "none"), "-version", testVersion, "-in", in}, nil},
		{"encrypted key without a password", []string{"-key", k.enc, "-version", testVersion, "-in", in}, nil},
		{"wrong password", []string{"-key", k.enc, "-version", testVersion, "-in", in}, map[string]string{"MINISIGN_PASSWORD": wrongWord}},
		{"password for a plain key", []string{"-key", k.plain, "-version", testVersion, "-in", in}, map[string]string{"MINISIGN_PASSWORD": secretWord}},
		{"missing installer", []string{"-key", k.plain, "-version", testVersion, "-in", filepath.Join(k.dir, "none.exe")}, nil},
		{"empty installer", []string{"-key", k.plain, "-version", testVersion, "-in", empty}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if rc := run(tc.args, env(tc.env), &stdout, &stderr); rc == 0 {
				t.Fatal("rc = 0, want a failure")
			}
			for _, secret := range []string{secretWord, wrongWord} {
				if strings.Contains(stderr.String()+stdout.String(), secret) {
					t.Errorf("output leaks %q: %s", secret, stderr.String())
				}
			}
			if _, err := os.Stat(in + ".minisig"); err == nil {
				t.Error("a signature file was written on failure")
			}
		})
	}
}

func TestTrustedCommentMatchesTheApp(t *testing.T) {
	if got, want := trustedComment("0.1.1-nightly.20261102+abc1234", "a.exe"), service.UpdateTrustedComment("0.1.1-nightly.20261102+abc1234", "a.exe"); got != want {
		t.Errorf("trustedComment = %q, app expects %q", got, want)
	}
}

// cheapEncryptedKey は minisign の「パスワード付きの秘密鍵」の形式（scrypt + BLAKE2b のチェックサム）を、軽い scrypt のコスト
// （ops = 2^15、mem = 2^19。DecryptKey が読むパラメータ。minisign.EncryptKey は固定で 1 GiB・数秒かかり、-race の CI で遅いため使わない）で作る。
// 復号は本物の minisign.DecryptKey が行う（ツールが使う経路そのもの）。
func cheapEncryptedKey(t *testing.T, password string, priv minisign.PrivateKey) []byte {
	t.Helper()
	const (
		ops, mem = 1 << 15, 1 << 19
		// convertScryptParameters(ops, mem) = (N = 512, r = 8, p = 2)
		scryptN, scryptR, scryptP = 512, 8, 2
	)
	plain, err := priv.MarshalText() // 暗号化されていない形（158 バイトの base64）。54 バイト目から 鍵 ID（8）・Ed25519 の秘密鍵（64）
	if err != nil {
		t.Fatal(err)
	}
	raw := decodeKey(t, plain)
	id, secret := raw[54:62], raw[62:126]

	var salt [32]byte
	if _, err := rand.Read(salt[:]); err != nil {
		t.Fatal(err)
	}
	keystream, err := scrypt.Key([]byte(password), salt[:], scryptN, scryptR, scryptP, 104)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := append(append([]byte{}, id...), secret...) // 72 バイト
	message := append([]byte{0x45, 0x64}, plaintext...)     // EdDSA の定数 + 平文
	sum := blake2b.Sum256(message)
	cipher := append(append([]byte{}, plaintext...), sum[:]...)
	for i := range cipher {
		cipher[i] ^= keystream[i]
	}

	out := make([]byte, 158)
	binary.LittleEndian.PutUint16(out[0:], minisign.EdDSA)
	binary.LittleEndian.PutUint16(out[2:], 0x6353) // "Sc"
	binary.LittleEndian.PutUint16(out[4:], 0x3242) // "B2"
	copy(out[6:38], salt[:])
	binary.LittleEndian.PutUint64(out[38:], ops)
	binary.LittleEndian.PutUint64(out[46:], mem)
	copy(out[54:], cipher)
	return []byte("untrusted comment: minisign encrypted secret key\n" + base64.StdEncoding.EncodeToString(out) + "\n")
}

func decodeKey(t *testing.T, text []byte) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	raw, err := base64.StdEncoding.DecodeString(lines[len(lines)-1])
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
