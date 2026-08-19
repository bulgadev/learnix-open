-- 0011_standalone_tests.sql: indexes for quizzes created outside a study.

CREATE INDEX IF NOT EXISTS idx_quizzes_user_standalone
    ON quizzes(user_id, study_id, created_at DESC);
