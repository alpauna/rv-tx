CREATE TABLE IF NOT EXISTS nodes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name          text NOT NULL UNIQUE,
    public_key    text NOT NULL UNIQUE,
    mesh_ip       inet NOT NULL UNIQUE,
    last_endpoint text,
    last_seen     timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
