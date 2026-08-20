CREATE TABLE application_source_owners (
    application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX application_source_owners_user ON application_source_owners(owner_user_id, application_id);
INSERT INTO application_source_owners(application_id, owner_user_id)
SELECT s.application_id, c.owner_user_id
FROM application_sources s JOIN source_connections c ON c.id = s.connection_id
WHERE s.source_type = 'github';
