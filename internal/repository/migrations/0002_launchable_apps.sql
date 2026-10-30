-- v2（M1.5）: 起動許可リスト（docs/architecture/er.md「launchable_apps」）。他の表への外部キーは無い。
-- 時刻は UTC の Unix ミリ秒。登録数の上限（50。NFR-C06）は DB の制約にせず、Create が同一トランザクションで守る。
-- label の UNIQUE は NOCASE（ASCII の A〜Z だけを畳み込む。全角英字などは別の値）。

CREATE TABLE launchable_apps (
    id          TEXT PRIMARY KEY,
    label       TEXT NOT NULL,
    exe_path    TEXT NOT NULL,
    args_json   TEXT NOT NULL DEFAULT '[]',
    working_dir TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (label COLLATE NOCASE),
    CHECK (length(label) BETWEEN 1 AND 64),
    CHECK (length(exe_path) > 0),
    CHECK (json_valid(args_json) AND json_type(args_json) = 'array')
);
