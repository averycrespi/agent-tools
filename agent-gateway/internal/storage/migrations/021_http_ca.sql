ALTER TABLE keyring_authority_fences RENAME TO keyring_authority_fences_previous;

CREATE TABLE keyring_authority_fences (
    owner TEXT NOT NULL CHECK (
        length(owner) = 26 AND
        substr(owner, 1, 1) BETWEEN '0' AND '7' AND
        owner NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'
    ),
    kind TEXT NOT NULL CHECK (kind IN ('static_credential', 'oauth_client', 'oauth_tokens', 'http_credential', 'http_ca')),
    PRIMARY KEY (owner, kind)
) STRICT;

INSERT INTO keyring_authority_fences SELECT owner, kind FROM keyring_authority_fences_previous;
DROP TABLE keyring_authority_fences_previous;

CREATE TABLE http_ca (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL CHECK (revision >= 0),
    handle TEXT,
    certificate BLOB,
    CHECK (handle IS NULL OR (revision > 0 AND certificate IS NOT NULL)),
    CHECK (certificate IS NULL OR length(certificate) BETWEEN 1 AND 4096)
) STRICT;

INSERT INTO http_ca (singleton, revision) VALUES (1, 0);

INSERT INTO schema_migrations (version, name) VALUES (21, 'http_ca');
