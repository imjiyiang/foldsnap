CREATE TABLE backup_previews(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE INDEX idx_preview_plan ON backup_previews(json_extract(data,'$.planId'));
