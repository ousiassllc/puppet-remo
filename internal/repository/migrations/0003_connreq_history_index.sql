-- v3: 接続要求の履歴（GUI の「接続要求 > 履歴」。docs/architecture/er.md「connection_requests」）。
-- 結果（status）と処理日時（decided_at）は v1 から connection_requests が持っているため、列は足さない（既存の行は無変更）。
-- 履歴は first_seen_at の新しい順にページ分けして読むので、全件の並べ替えを避けるための索引だけを足す。
CREATE INDEX ix_connreq_first_seen
    ON connection_requests (first_seen_at DESC, id DESC);
