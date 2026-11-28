package platform

import (
	"context"
	"time"
)

// FileStore は、ホストが決めたフォルダ（ルート）の中だけでファイルを置く・取り出す・一覧する・消す窓口（#264。MCP の
// put_file / get_file / list_files / delete_file の実体。許可・上限・リースの判定は service.FileTransfer）。
//
// すべての操作は root（絶対パス）と、そこからの相対パス rel を取る。rel は domain.CleanTransferPath を通った形（"/" 区切り・
// ".." なし・ドライブ指定なし）であること。実装は、それに加えて次を保証する（呼び出し側の検査に頼らない）:
//   - root が無ければ作る（Put）。root 自体は、ホストが選んだ場所なので、リンクを経由していてもよい（実体のパスを以後の基準にする）
//   - rel の途中・末尾に、シンボリックリンク・ジャンクション・リパースポイントがあれば拒否する（domain.ErrInvalidArgument）。
//     実体のパスがルートの外に出るものも同じ。開いたあとにもう一度確かめ、検査と使用の間のすり替えを狭める
//   - 通常のファイルとディレクトリだけを扱う（デバイス・ソケットなどは拒否）
//
// 無いファイルは domain.ErrFileNotFound。エラーにパスは含めない。どの goroutine からも呼べるが、同じルートへの書き込みの直列化は呼び出し側が行う。
type FileStore interface {
	// Put は rel へ data を書く。Offset が 0 なら新しく作る（同名の通常ファイルは置き換える。途中のフォルダは作る）。
	// Offset が 0 より大きいときは、すでにあるファイルの大きさがちょうど Offset のときだけ、その末尾へ追記する（分割した転送。
	// 違えば domain.ErrConflict）。書いたあとのファイルの大きさが MaxFileBytes を超えれば domain.ErrFileTooLarge、
	// ルート全体の大きさが MaxTotalBytes を超えれば domain.ErrFileStoreFull（どちらも何も書かない）。
	Put(ctx context.Context, root string, req FilePut) (FileInfo, error)
	// Get は rel のファイルの [offset, offset+length) を読む（ファイルの末尾を超える分は切り詰める。length の上限は呼び出し側が決める）。
	// offset がファイルの大きさを超えれば domain.ErrInvalidArgument（ちょうど大きさなら空）。
	// FileInfo は読んだ範囲ではなく、ファイル全体の大きさと SHA-256。
	Get(ctx context.Context, root, rel string, offset int64, length int) ([]byte, FileInfo, error)
	// List は rel のディレクトリ（空文字ならルート）の直下を、名前の昇順で最大 limit 件返す（再帰しない）。
	// limit を超える分は切り捨て、truncated を true にする。リンクの項目は返さない。
	List(ctx context.Context, root, rel string, limit int) (entries []FileEntry, truncated bool, err error)
	// Delete は rel のファイルまたは空のディレクトリを消す。中身のあるディレクトリは domain.ErrConflict。消したものの情報を返す（SHA-256 は空）。
	Delete(ctx context.Context, root, rel string) (FileInfo, error)
	// Resolve は rel の実在する通常ファイルの、実体の絶対パスを返す（ルートの中であることと、リンクを経由しないことを確かめたもの）。
	// 他の機能がホストの上でそのファイルを参照するため（#265）。読み書きはしない。
	Resolve(ctx context.Context, root, rel string) (string, error)
	// TotalSize はルート以下の通常ファイルの大きさの合計。ルートが無ければ 0。
	TotalSize(ctx context.Context, root string) (int64, error)
}

// FilePut は FileStore.Put の指定。
type FilePut struct {
	Rel           string
	Offset        int64
	Data          []byte
	MaxFileBytes  int64 // 書いたあとのファイルの大きさの上限
	MaxTotalBytes int64 // 書いたあとのルート全体の大きさの上限
}

// FileInfo はファイル 1 つの大きさと、中身の SHA-256（16 進の小文字）。
type FileInfo struct {
	Rel    string
	Size   int64
	SHA256 string
}

// FileEntry は List の 1 項目。Rel はルートからの相対パス（"/" 区切り）。
type FileEntry struct {
	Rel     string
	IsDir   bool
	Size    int64 // ディレクトリは 0
	ModTime time.Time
}
