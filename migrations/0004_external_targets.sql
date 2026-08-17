ALTER TABLE resources DROP CONSTRAINT resources_protocol_check;
ALTER TABLE resources ADD CONSTRAINT resources_protocol_check CHECK (protocol IN ('http', 'tcp', 'udp'));

ALTER TABLE resource_targets ALTER COLUMN node_id DROP NOT NULL;
ALTER TABLE resource_targets ADD COLUMN address text;
ALTER TABLE resource_targets ADD CONSTRAINT resource_targets_target_check
    CHECK ((node_id IS NOT NULL) != (address IS NOT NULL));
