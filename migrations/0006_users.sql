CREATE TABLE IF NOT EXISTS users (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email                 text NOT NULL UNIQUE,
    password_hash         text,
    role                  text NOT NULL DEFAULT 'viewer',
    invite_token          text UNIQUE,
    invite_token_expires  timestamptz,
    reset_token           text UNIQUE,
    reset_token_expires   timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now()
);
