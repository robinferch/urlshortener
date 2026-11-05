-- Dashboards scan visits by time across many links; BRIN is tiny and fits append-only timestamps.
CREATE INDEX visits_ts_brin_idx ON visits USING BRIN (ts);
ALTER TABLE import_jobs ADD COLUMN heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now();
