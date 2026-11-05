-- Links imported without some of their Shlink redirect rules (e.g. language or query-param rules).
ALTER TABLE import_jobs ADD COLUMN unsupported INT NOT NULL DEFAULT 0;
