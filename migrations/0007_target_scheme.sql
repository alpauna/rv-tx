ALTER TABLE resources ADD COLUMN target_scheme text NOT NULL DEFAULT 'http';
ALTER TABLE resources ADD COLUMN target_skip_verify boolean NOT NULL DEFAULT false;
