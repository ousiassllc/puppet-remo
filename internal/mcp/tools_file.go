package mcp

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// ファイル転送のツール（#264。FR-M27。API 仕様 §3.3「ファイル転送」）。
// 扱えるのは、ホストが設定画面で決めた置き場所（既定 %LOCALAPPDATA%\PuppetRemo\inbox）の中だけで、パスは置き場所からの相対パスだけ。
// 置き場所・上限をこの層（MCP）から知る・変える経路は無い（入出力・エラーにホストの絶対パスは出ない）。
// 検査（キルスイッチ・リース・パス・上限）と監査（tool.put_file など。ファイルの中身は記録しない）はすべて service.FileTransfer の仕事で、
// この層は base64 の変換と出力の整形だけをする。FileTransfer が自分で監査するので addTool で登録する（addAuditedTool では二重に記録される）。

// 説明に付ける共通の注意。
const fileToolNote = " Works only inside the transfer folder the host owner chose (never exposed; paths are relative to it, " +
	"no .., absolute, drive-letter, UNC or alternate-stream paths, and no symbolic links, junctions or reparse points). " +
	"The host owner sets the size limits. Needs the control lease (acquired implicitly)."

type putFileIn struct {
	Path          string `json:"path" jsonschema:"path relative to the transfer folder, for example logs/app.log (folders are created as needed)"`
	ContentBase64 string `json:"content_base64" jsonschema:"file content, base64 (standard alphabet), at most 524288 bytes before encoding per call"`
	Offset        int64  `json:"offset,omitempty" jsonschema:"0 (default) creates the file, replacing an existing one. A larger value appends: it must equal the current size of the file, for sending a larger file in chunks"`
}

type getFileIn struct {
	Path   string `json:"path" jsonschema:"path relative to the transfer folder"`
	Offset int64  `json:"offset,omitempty" jsonschema:"byte offset to start reading at (default 0)"`
	Length int    `json:"length,omitempty" jsonschema:"bytes to read, at most 524288 (default and maximum 524288). Read larger files in chunks until eof is true"`
}

type listFilesIn struct {
	Path string `json:"path,omitempty" jsonschema:"folder relative to the transfer folder (omit for the transfer folder itself)"`
}

type deleteFileIn struct {
	Path string `json:"path" jsonschema:"file or empty folder, relative to the transfer folder"`
}

// putFileBody は put_file の出力。size と sha256 は、書いたあとのファイル全体のもの（分割して送ったときは、ここまでの全体）。
type putFileBody struct {
	OK     bool   `json:"ok"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// getFileBody は get_file の出力。size と sha256 はファイル全体のもの（読んだ範囲ではない）。
type getFileBody struct {
	Path          string `json:"path"`
	Offset        int64  `json:"offset"`
	Length        int    `json:"length"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	EOF           bool   `json:"eof"`
	ContentBase64 string `json:"content_base64"`
}

type fileEntryBody struct {
	Path     string `json:"path"`
	Type     string `json:"type"` // file | dir
	Size     int64  `json:"size"`
	Modified string `json:"modified"` // RFC 3339（UTC）
}

type listFilesBody struct {
	Path      string          `json:"path"`
	Entries   []fileEntryBody `json:"entries"`
	Truncated bool            `json:"truncated"`
}

type deleteFileBody struct {
	OK   bool   `json:"ok"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// maxBase64Len は 1 回で運べる最大のバイト数を base64（パディングあり）にした長さ。これより長い本文は、デコードせずに断る。
const maxBase64Len = (domain.MaxFileChunkBytes + 2) / 3 * 4

// addFileTools はファイル転送のツールを登録する: FR-M27。いずれもリースが要る（暗黙に取得する。leaseAcquiringTools）。
func (s *Server) addFileTools() {
	addTool(s, "put_file",
		"Put a file into the host's transfer folder (for example a build to test or a config file). The content goes in as base64, "+
			"at most 524288 bytes per call; send a larger file in chunks with offset (0 creates or replaces the file, a later chunk appends at offset = the size so far). "+
			"Returns the size and SHA-256 (lowercase hex) of the whole file as written, so you can verify it. "+
			"Fails with file_too_large (over the per-file limit), file_store_full (over the folder's total limit), invalid_argument (bad path or content), "+
			"conflict (wrong offset), lease_held_by_other, kill_switch_active."+fileToolNote,
		false, func(ctx context.Context, who domain.Actor, in putFileIn) (toolResult, error) {
			if len(in.ContentBase64) > maxBase64Len {
				return toolResult{}, domain.ErrInvalidArgument.WithMsg("content is too large for one call: send the file in chunks of at most 524288 bytes, using offset")
			}
			data, err := base64.StdEncoding.DecodeString(in.ContentBase64)
			if err != nil {
				return toolResult{}, domain.ErrInvalidArgument.WithMsg("content_base64 is not valid base64")
			}
			res, err := s.files.Put(ctx, who, service.FilePutRequest{Path: in.Path, Offset: in.Offset, Data: data})
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: putFileBody{OK: true, Path: res.Path, Size: res.Size, SHA256: res.SHA256}}, nil
		})

	addTool(s, "get_file",
		"Get a file from the host's transfer folder (for example a log). The content comes back as base64, at most 524288 bytes per call; "+
			"read a larger file in chunks by offset until eof is true. size and sha256 (lowercase hex) are those of the whole file, so you can verify the assembled content. "+
			"Fails with file_not_found, invalid_argument (bad path, a folder, offset beyond the end), lease_held_by_other, kill_switch_active."+fileToolNote,
		true, func(ctx context.Context, who domain.Actor, in getFileIn) (toolResult, error) {
			chunk, err := s.files.Get(ctx, who, service.FileGetRequest{Path: in.Path, Offset: in.Offset, Length: in.Length})
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: getFileBody{
				Path: chunk.Path, Offset: chunk.Offset, Length: len(chunk.Data), Size: chunk.Size, SHA256: chunk.SHA256, EOF: chunk.EOF,
				ContentBase64: base64.StdEncoding.EncodeToString(chunk.Data),
			}}, nil
		})

	addTool(s, "list_files",
		"List the files and folders directly inside a folder of the host's transfer folder (not recursive), sorted by name, at most 1000 entries (truncated tells when more exist). "+
			"Symbolic links and other special entries are not listed. An empty or missing transfer folder lists as empty. "+
			"Fails with file_not_found (no such folder), invalid_argument, lease_held_by_other, kill_switch_active."+fileToolNote,
		true, func(ctx context.Context, who domain.Actor, in listFilesIn) (toolResult, error) {
			listing, err := s.files.List(ctx, who, in.Path)
			if err != nil {
				return toolResult{}, err
			}
			out := listFilesBody{Path: in.Path, Entries: make([]fileEntryBody, 0, len(listing.Entries)), Truncated: listing.Truncated}
			for _, e := range listing.Entries {
				kind := "file"
				if e.IsDir {
					kind = "dir"
				}
				out.Entries = append(out.Entries, fileEntryBody{Path: e.Path, Type: kind, Size: e.Size, Modified: e.Modified.UTC().Format(time.RFC3339)})
			}
			return toolResult{Structured: out}, nil
		})

	addTool(s, "delete_file",
		"Delete a file, or an empty folder, from the host's transfer folder. A folder that still has content is not deleted (conflict). "+
			"Fails with file_not_found, invalid_argument, conflict, lease_held_by_other, kill_switch_active."+fileToolNote,
		false, func(ctx context.Context, who domain.Actor, in deleteFileIn) (toolResult, error) {
			res, err := s.files.Delete(ctx, who, in.Path)
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: deleteFileBody{OK: true, Path: res.Path, Size: res.Size}}, nil
		})
}
