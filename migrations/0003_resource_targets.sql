CREATE TABLE resource_targets (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_id uuid NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    node_id     uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    port        integer NOT NULL,
    role        text NOT NULL DEFAULT 'primary' CHECK (role IN ('primary', 'backup'))
);

INSERT INTO resource_targets (resource_id, node_id, port, role)
SELECT id, target_node_id, target_port, 'primary' FROM resources;

ALTER TABLE resources DROP COLUMN target_node_id;
ALTER TABLE resources DROP COLUMN target_port;
