ALTER TABLE invocations ADD COLUMN failure_diagnostics TEXT
    CHECK (failure_diagnostics IS NULL OR (
        terminal_class IS NOT NULL AND terminal_class <> 'succeeded' AND
        length(CAST(failure_diagnostics AS BLOB)) <= 512 AND
        json_valid(failure_diagnostics) AND json_type(failure_diagnostics) = 'object'
    ));

INSERT INTO schema_migrations (version, name) VALUES (17, 'failure_diagnostics');
