-- LinkCode database migration: add total_cost to sessions
ALTER TABLE sessions ADD COLUMN total_cost DOUBLE NOT NULL DEFAULT 0 COMMENT '累计消耗费用 (calculated from tokens * pricing)';
