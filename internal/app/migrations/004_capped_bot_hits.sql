-- Bot hits on links with max_visits use up the quota but must not count as human visits in analytics.
ALTER TABLE links ADD COLUMN capped_bot_hits BIGINT NOT NULL DEFAULT 0;
