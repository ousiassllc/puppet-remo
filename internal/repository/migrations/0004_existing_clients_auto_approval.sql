-- v4: M3 の承認モードの移行（FR-L07・FR-H03。docs/architecture/er.md「clients」）。
-- M1・M2 の間に登録された既存のクライアントは、承認モードを選べなかったため DB の既定値 'always' で保存されている。
-- 承認ダイアログはホストの手元でしか押せないので、そのままだと外からの操作がすべて 60 秒で拒否される。
-- 既存のクライアント（このマイグレーション時点の行）は 'auto'（自動許可）へ移す。
-- 列の既定値（'always'）は変えない: v4 の後に登録されるクライアントは、承認モードを選ばなければ 'always'（常時承認）のまま。
-- approved_at は 'auto' では参照しないので触らない。マイグレーションは 1 度しか適用されないため、後から 'always' に戻した行は再び書き換わらない。
UPDATE clients SET approval_mode = 'auto' WHERE approval_mode = 'always';
