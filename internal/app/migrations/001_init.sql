CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    password_hash TEXT,
    oidc_subject  TEXT UNIQUE,
    is_admin      BOOLEAN NOT NULL DEFAULT false,
    disabled      BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

CREATE TABLE sessions (
    token_hash BYTEA PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE api_keys (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    name         TEXT NOT NULL,
    key_hash     BYTEA NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);

CREATE TABLE domains (
    id           BIGSERIAL PRIMARY KEY,
    host         TEXT NOT NULL UNIQUE,
    scheme       TEXT NOT NULL DEFAULT 'https' CHECK (scheme IN ('http', 'https')),
    fallback_url TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE domain_members (
    domain_id BIGINT NOT NULL REFERENCES domains ON DELETE CASCADE,
    user_id   BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    role      TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    PRIMARY KEY (domain_id, user_id)
);
CREATE INDEX domain_members_user_idx ON domain_members (user_id);

CREATE TABLE links (
    id            BIGSERIAL PRIMARY KEY,
    domain_id     BIGINT NOT NULL REFERENCES domains ON DELETE CASCADE,
    slug          TEXT NOT NULL,
    target_url    TEXT NOT NULL,
    ios_url       TEXT NOT NULL DEFAULT '',
    android_url   TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    tags          TEXT[] NOT NULL DEFAULT '{}',
    password_hash TEXT,
    valid_since   TIMESTAMPTZ,
    valid_until   TIMESTAMPTZ,
    max_visits    INT,
    forward_query BOOLEAN NOT NULL DEFAULT true,
    enabled       BOOLEAN NOT NULL DEFAULT true,
    visit_count   BIGINT NOT NULL DEFAULT 0,
    created_by    BIGINT REFERENCES users ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (domain_id, slug)
);
CREATE INDEX links_created_by_idx ON links (created_by);
CREATE INDEX links_created_at_idx ON links (domain_id, created_at DESC);
CREATE INDEX links_tags_idx ON links USING GIN (tags);

CREATE TABLE visits (
    id       BIGSERIAL PRIMARY KEY,
    link_id  BIGINT NOT NULL REFERENCES links ON DELETE CASCADE,
    ts       TIMESTAMPTZ NOT NULL,
    referrer TEXT NOT NULL DEFAULT '',
    country  TEXT NOT NULL DEFAULT '',
    browser  TEXT NOT NULL DEFAULT '',
    os       TEXT NOT NULL DEFAULT '',
    device   TEXT NOT NULL DEFAULT '',
    bot      BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX visits_link_ts_idx ON visits (link_id, ts);

CREATE TABLE import_jobs (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users ON DELETE CASCADE,
    source      TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'running',
    total       INT NOT NULL DEFAULT 0,
    imported    INT NOT NULL DEFAULT 0,
    skipped     INT NOT NULL DEFAULT 0,
    visits      INT NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);
