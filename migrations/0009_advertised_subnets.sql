ALTER TABLE nodes ADD COLUMN advertised_subnets text[] NOT NULL DEFAULT '{}';
