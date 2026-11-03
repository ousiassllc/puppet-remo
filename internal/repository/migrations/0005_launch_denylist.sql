-- v5: 起動の方式（launch.mode）と拒否リスト（docs/architecture/er.md「launch_denylist」）。
-- 方式は settings の `launch.mode`（未設定は allowlist）に持つので、既存のインストールは行を足さなくても allowlist のまま動く。
-- 拒否リストは denylist 方式のときだけ効く。kind は 'path'（実体の絶対パス）か 'name'（実行ファイル名）。
-- dedup_key は kind と value を大文字小文字・区切りの違いを畳んだ形（domain.DeniedKey）で、重複登録を防ぐ。
-- 登録数の上限（200）は DB の制約にせず、Create が同一トランザクションで守る。時刻は UTC の Unix ミリ秒。

CREATE TABLE launch_denylist (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('path', 'name')),
    value      TEXT NOT NULL,
    dedup_key  TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (length(value) > 0)
);
