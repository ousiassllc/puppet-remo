// minisign-sign は、リリースのインストーラに minisign 形式（Ed25519）の署名ファイル（<インストーラ>.minisig）を作る
// （docs/operations/release.md「署名」）。アプリの更新（service.Updater）は、この署名を埋め込みの公開鍵で検証してから適用する。
//
// 署名は、インストーラのバイト列全体に対する事前ハッシュ形式（minisign の既定）で、trusted comment に
// `version=<版> file=<インストーラのファイル名>` を入れる（署名済みの古いインストーラを新しい版へ付け替える攻撃を、検証側が見破るため）。
//
// 使い方: minisign-sign -key <秘密鍵のファイル> -version <main.version> -in <インストーラ> [-out <署名ファイル>]
// 秘密鍵がパスワードで保護されているときは、環境変数 MINISIGN_PASSWORD に入れる（引数・標準出力・ログには出さない）。
// -key のファイルは呼び出し側（scripts/ci/sign-installer.sh）が Secret から作り、終わったら消す。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"aead.dev/minisign"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// trustedComment は署名の trusted comment。service.UpdateTrustedComment と同じ書式でなければならない
// （round trip のテストが、アプリの検証側と食い違わないことを確かめる）。
func trustedComment(version, file string) string {
	return fmt.Sprintf("version=%s file=%s", version, file)
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if err := sign(args, getenv, stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "minisign-sign:", err)
		return 1
	}
	return 0
}

func sign(args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("minisign-sign", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	keyPath := fs.String("key", "", "秘密鍵のファイル")
	version := fs.String("version", "", "アプリの版（exe の main.version）")
	in := fs.String("in", "", "署名するインストーラ")
	out := fs.String("out", "", "署名ファイル（既定は <in>.minisig）")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if *keyPath == "" || *version == "" || *in == "" || fs.NArg() != 0 {
		return errors.New("usage: minisign-sign -key <file> -version <version> -in <installer> [-out <file>]")
	}
	if *out == "" {
		*out = *in + ".minisig"
	}

	priv, err := readKey(*keyPath, getenv("MINISIGN_PASSWORD"))
	if err != nil {
		return err
	}
	f, err := os.Open(*in)
	if err != nil {
		return fmt.Errorf("open installer: %w", err)
	}
	defer func() { _ = f.Close() }()
	r := minisign.NewReader(f)
	if n, err := io.Copy(io.Discard, r); err != nil {
		return fmt.Errorf("read installer: %w", err)
	} else if n == 0 {
		return errors.New("the installer is empty")
	}
	file := filepath.Base(*in)
	sig := r.SignWithComments(priv, trustedComment(*version, file), "signature from puppet-remo release CI")
	if err := os.WriteFile(*out, sig, 0o644); err != nil {
		return fmt.Errorf("write signature: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "signed %s (version=%s) -> %s\n", file, *version, filepath.Base(*out))
	return nil
}

// readKey は minisign の秘密鍵のファイルを読む。password が空なら、パスワードなしの鍵として読む。
// エラーに鍵の内容・パスワードは含めない。
func readKey(path, password string) (minisign.PrivateKey, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return minisign.PrivateKey{}, errors.New("cannot read the secret key file")
	}
	var priv minisign.PrivateKey
	if password == "" {
		if err := priv.UnmarshalText(text); err != nil {
			return minisign.PrivateKey{}, errors.New("cannot use the secret key (is it password protected? set MINISIGN_PASSWORD)")
		}
		return priv, nil
	}
	priv, err = minisign.DecryptKey(password, text)
	if err != nil {
		return minisign.PrivateKey{}, errors.New("cannot decrypt the secret key (wrong MINISIGN_PASSWORD?)")
	}
	return priv, nil
}
