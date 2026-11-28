# コマンド実行の許可リスト（`run_command`。#265。FR-M32・FR-S25）

MCP の `run_command` は、**ホスト所有者が許可リストに書いたコマンドだけ**を実行し、終了コード・stdout・stderr を返します。QA でバージョン・ファイルの有無・プロセス・通信を確かめたり、ファイル転送で置いたインストーラーを実行したりするための道具です。**既定はオフ**で、オフのときは呼んでも `command_disabled` で拒否します。

## ON / OFF と許可リストの場所（ホスト所有者だけ）

ホスト GUI の設定画面の「コマンド実行」で設定します。**MCP・Web UI・リモートからは、ON/OFF も許可リストも変えられません**。

| 設定 | キー | 既定 | 意味 |
|---|---|---|---|
| リモートのクライアントに実行を認める | `command.enabled` | オフ | オフの間は `run_command` / `list_commands` が `command_disabled` で拒否される（操作権も取りに行かない） |
| 許可リストのファイル | `command.allowlist_path` | 空（`%LOCALAPPDATA%\puppet-remo\commands.json`） | ドライブ文字で始まるローカルの絶対パス（UNC・ドライブの直下・`..` は不可） |

- 許可リストのファイルは**ホスト所有者が手で書きます**（設定画面にエディタはありません）。呼び出しのたびに読み直すので、編集は再起動なしで反映されます。
- ファイルには**リモートが書き込めない場所**を使ってください。ファイル転送の置き場所（`file_transfer.root`。既定 `%LOCALAPPDATA%\PuppetRemo\inbox`）の中に許可リスト・実行ファイル・作業フォルダがあると、`put_file` で差し替えられるため、**使えません**（`command_allowlist_invalid`）。
- 1 項目でも不備があるとファイル全体を拒否します（一部だけ読み込むことはしません。`command_allowlist_invalid`）。理由は運用ログ（`%LOCALAPPDATA%\puppet-remo\logs`）に出ます。MCP の呼び出し側には返しません。
- すぐ使える例は [`command-allowlist.example.json`](command-allowlist.example.json)（全項目が `"enabled": false`。自分の環境に合わせてパスを直し、使う項目だけ `true` にする）。

## 許可リストの書き方

JSON（UTF-8）で、最上位は `{ "version": 1, "commands": [ ... ] }`。**未知のキーは不備として拒否**します（書き間違いに気づくため）。

```json
{
  "version": 1,
  "commands": [
    {
      "name": "process-find",
      "description": "List the processes with the given image name.",
      "executable": "C:\\Windows\\System32\\tasklist.exe",
      "args": ["/fi", "IMAGENAME eq {image}", "/fo", "csv", "/nh"],
      "params": {
        "image": { "type": "string", "pattern": "[A-Za-z0-9._-]{1,64}", "max_length": 64 }
      },
      "timeout_sec": 30,
      "max_output_bytes": 65536,
      "enabled": true
    }
  ]
}
```

### 項目（`commands` の要素）

| キー | 必須 | 意味 |
|---|---|---|
| `name` | 必須 | 呼び出しの名前。`[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`。重複は不可 |
| `description` | 任意 | `list_commands` で呼び出し側に見せる説明（200 文字まで） |
| `executable` | 必須 | 実行ファイルの**絶対パス**（ドライブ文字で始まるローカルのパス）。次は拒否: 相対パス、UNC（`\\host\share\...`）・デバイスパス（`\\?\`・`\\.\`）、`.` / `..` の要素、`:`（代替データストリーム）、ドライブの直下、末尾がドット・空白の名前、**スクリプト・ショートカット（`.bat` `.cmd` `.com` `.vbs` `.vbe` `.js` `.jse` `.wsf` `.wsh` `.ps1` `.psm1` `.hta` `.lnk` `.scr` `.pif` `.cpl` `.msc` `.jar`）** |
| `args` | 任意 | 引数のテンプレートの配列。1 要素が 1 つの引数になる。固定の文字列に、`{名前}` で `params` の値を差し込める（`{{` と `}}` は波括弧そのもの。例: curl の `%{{http_code}}`、PowerShell の `{{ exit 0 }}`）。1 つの差し込みは必ず 1 つの引数の中に入り、分割・再解釈されない |
| `params` | 任意 | 差し込みの定義（名前 → 型）。`args` で使うものをすべて定義し、定義したものはすべて使う。名前は `[a-z][a-z0-9_]{0,31}`、最大 16 個。呼び出し側は**すべて指定する**（省略・既定値は無い） |
| `working_dir` | 任意 | 作業フォルダ（絶対パス）。省略はアプリの既定 |
| `timeout_sec` | 任意 | タイムアウト（1〜600 秒。既定 30） |
| `max_output_bytes` | 任意 | stdout・stderr それぞれの保持の上限（1〜524288。既定 65536） |
| `enabled` | 任意 | **省略は無効**。`true` の項目だけ実行でき、`list_commands` に出る |

### 差し込み（`params`）の型

| `type` | 指定するキー | 受け付ける値 |
|---|---|---|
| `int` | `min`・`max`（必須） | `min`〜`max` の整数（JSON の数値、または数字だけの文字列） |
| `enum` | `values`（必須。64 個まで） | `values` のどれか 1 つ（完全一致。大文字小文字を区別する） |
| `string` | `pattern`（必須）・`max_length`（既定 256・最大 1024） | `pattern`（正規表現。Go の RE2。**値の全体**に一致する必要がある）に合う 1〜`max_length` 文字。制御文字・NUL は不可。`args` の要素の**先頭**に置く差し込みでは、`-` や `/` で始まる値も拒否する（オプションの注入を防ぐ） |
| `inbox_path` | `extensions`（必須。`[".msi"]` のように小文字・ドット付き） | ファイル転送の置き場所の中にある**実在する通常ファイル**の相対パス（拡張子が `extensions` のどれか）。実行のときに、`..`・絶対パス・UNC・ドライブ指定・`:`・予約名・リンク経由・置き場所の外を `put_file` と同じ規則で拒否し、**実体の絶対パスに置き換えて**渡す |

型・範囲・パターンに合わない値、足りない引数、定義にない引数は `invalid_argument`（実行しません。メッセージに値は載せません）。

### シェルを使わない

`run_command` は **シェルを使わず**、`executable` を引数の配列で直接起動します（`cmd /c`・`powershell -Command` で包みません）。パイプ・リダイレクト・`&`・`$(...)` などは、渡しても**ただの文字**で、解釈されません。API には、コマンドライン文字列・実行ファイル・シェルを渡す入力そのものがありません（入力は `name` と `args` だけ。余計なキーは入力スキーマが拒否し、監査に残ります）。

そのうえで、許可リストを書くときの約束:

- **`cmd.exe`・`powershell.exe`・`pwsh.exe`・`wscript.exe`・`cscript.exe`・`mshta.exe`・`wsl.exe`・`bash.exe` など、シェルやスクリプトホスト（インタープリター）を `executable` にする項目では、`string` と `inbox_path` の差し込みを使えません**（読み込みで拒否します）。固定の引数・`int`・`enum` だけにしてください。呼び出し側が実行する内容を決められないようにするためです。例の `file-version` は、`enum` で選ぶ固定のパスだけを PowerShell の固定のコマンドに差し込んでいます。
- **`.bat`・`.cmd` は実行ファイルにできません**。cmd.exe が引数を解釈し直すため、Go が行う引数のクォートも、上の型の検査も効かなくなるからです。
- `inbox_path` で渡したファイルを**実行ファイルとして直接起動することはできません**（`executable` は固定）。`msiexec /i {file}` のように、固定の実行ファイルの引数として使ってください。
- 呼び出し側が書き込める置き場所の外の、**管理者だけが書き換えられる場所の実行ファイル**（`C:\Windows\System32`・`C:\Program Files` など）を使ってください。

## 実行の仕組み

- 標準入力は空、ウィンドウは出しません。環境変数は、動作に必要な最小限の名前（`SystemRoot`・`PATH`・`TEMP`・`USERPROFILE` など）だけを引き継ぎます（トークンなどの秘密を含みうる他の変数は渡しません）。
- **タイムアウト**を過ぎたら、Windows ではジョブオブジェクトで**子孫プロセスごと**終了させ、`timed_out: true`・`exit_code: -1` で返します（エラーではなく結果）。このアプリが落ちたときもジョブごと終わります。
- **出力の上限**を超えた分は捨て、`stdout_truncated` / `stderr_truncated` を `true` にして返します。
- 出力は UTF-8 の文字列で返します（`stdout_encoding` / `stderr_encoding` = `utf-8`）。BOM つきの UTF-16 は UTF-8 に直します（`utf-16le`）。それ以外（日本語環境の旧コードページ CP932 など）は `binary`: 文字列は不正なバイトを U+FFFD にしたもので、**元のバイト列は `stdout_base64` / `stderr_base64` で返します**（欠けません）。
- 非 0 の終了コードはエラーではなく結果です。同時に走らせられるのは 4 つまで（超えると `conflict`）。
- キルスイッチの発動で、実行中のコマンドも終了させます。

## 操作権・監査

- `run_command` は**操作権（リース）が要り**（入力系のツールと同じく暗黙に取得。承認が要るクライアントは承認を待ちます）、キルスイッチ中は使えません。ウィンドウを選んだ操作権（入力スコープ）では使えません（`input_blocked`・`detail.reason` = `command_out_of_scope`）。`list_commands` は操作権が要りません（ON/OFF とキルスイッチは見ます）。
- ホストの実入力による一時停止（FR-M30）の**対象外**です（入力を注入しないため）。ただしインストーラーなどはホストの画面にウィンドウを出すことがあります。
- 呼び出しごとに監査ログへ `tool.run_command` を残します: 項目名・引数の値（1 つ 128 文字で切る）・終了コード・タイムアウトしたか・stdout / stderr の**大きさ**と切り詰めたか・結果・所要時間。**stdout / stderr の中身と実行ファイルのパスは記録しません**。拒否した呼び出し（オフ・名前なし・引数の不備・操作権なし）も残ります。string の差し込みには、秘密（トークンなど）を渡さないでください（値が監査に残ります）。

## 例

[`command-allowlist.example.json`](command-allowlist.example.json) の項目（すべて `"enabled": false`）:

| 名前 | 内容 | 実行ファイル |
|---|---|---|
| `app-version` | 試験対象のアプリの `--version` | アプリ自身 |
| `file-version` | 指定したファイル（`enum`）のファイルバージョン | `powershell.exe`（固定のコマンド） |
| `file-exists` | 指定したパス（`enum`）が在れば終了コード 0、無ければ 1 | `powershell.exe`（固定のコマンド） |
| `process-list` / `process-find` | プロセス一覧（`tasklist /fo csv /nh`）/ イメージ名で絞り込み | `tasklist.exe` |
| `listening-ports` | `netstat -ano` | `netstat.exe` |
| `http-check` | `http://<host>:<port>/` の HTTP ステータス（`curl.exe --max-time 5`。PowerShell の `Test-NetConnection` は使わない） | `curl.exe` |
| `ping-host` | `ping -n 2 <host>` | `PING.EXE` |
| `install-msi` | ファイル転送の置き場所にある `.msi` を `msiexec /i <file> /qn /norestart` で実行 | `msiexec.exe` |
| `verify-signature` | 置き場所にあるビルド（`.exe`・`.msi`・`.dll`）の署名を検証 | `signtool.exe` |

呼び出しの例（MCP）: `list_commands` で名前と引数の定義を見て、`run_command` に `{ "name": "http-check", "args": { "host": "localhost", "port": 8765 } }` を渡す。インストールは、`put_file` で `builds/app.msi` を置いてから `{ "name": "install-msi", "args": { "file": "builds/app.msi" } }`。

関連: [API 仕様 §3.3「コマンド実行」](../api/endpoints.md)、[実機確認の手順](smoke-windows.md)。
