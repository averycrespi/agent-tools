CREATE TABLE traffic_selection (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    generation TEXT CHECK (generation IS NULL OR (length(generation) = 26 AND generation NOT GLOB '*[^0-9A-HJKMNP-TV-Z]*'))
) STRICT;

INSERT INTO traffic_selection VALUES (1, NULL);

INSERT INTO schema_migrations (version, name) VALUES (17, 'traffic_selection');
