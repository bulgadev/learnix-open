-- 0004_email_canon.sql: canonicalize emails to lowercase.
--
-- Emails are stored lowercase (registration/login normalize input). The
-- UNIQUE(email) constraint then guarantees one account per address even
-- across case variants — closing the admin-takeover path where a second
-- account registered as a case variant of ADMIN_EMAIL would fold to the
-- same identity. Idempotent: re-running on already-lowercase data is a no-op.

UPDATE users SET email = lower(email);
