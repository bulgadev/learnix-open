-- 0008_public_handles.sql: editable public usernames with stable four-digit tags.
-- The columns are added by ensurePublicHandleColumns so this migration remains
-- safe when the full migration set is executed on every application start.
SELECT 1;
