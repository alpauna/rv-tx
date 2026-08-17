CREATE TABLE IF NOT EXISTS resources (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text NOT NULL UNIQUE,
    protocol       text NOT NULL CHECK (protocol IN ('http', 'tcp')),
    domain         text,
    target_node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    target_port    integer NOT NULL,
    entry_point    text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
