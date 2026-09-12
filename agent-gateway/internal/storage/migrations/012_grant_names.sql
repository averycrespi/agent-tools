ALTER TABLE grants ADD COLUMN name TEXT NOT NULL DEFAULT 'Grant' CHECK (
    length(CAST(name AS BLOB)) BETWEEN 1 AND 256
);

UPDATE grants SET name = 'Grant ' || id;

INSERT INTO schema_migrations (version, name)
VALUES (12, 'grant_names');
