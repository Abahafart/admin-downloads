-- Scope of the quota a download is waiting on / failed with ('' | 'ip' |
-- 'account' | 'file'), shown by the UI ("rotar la IP no ayuda" for file).

-- +goose Up
ALTER TABLE downloads ADD COLUMN quota_scope TEXT NOT NULL DEFAULT ''
    CHECK (quota_scope IN ('', 'ip', 'account', 'file'));

-- +goose Down
ALTER TABLE downloads DROP COLUMN quota_scope;
