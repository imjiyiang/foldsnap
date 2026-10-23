ALTER TABLE source_scans ADD COLUMN projection_tag TEXT NOT NULL DEFAULT '';
ALTER TABLE source_scans ADD COLUMN scan_mode TEXT NOT NULL DEFAULT 'full';
ALTER TABLE source_scans ADD COLUMN walked_paths INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_scans ADD COLUMN last_full_scan_ns INTEGER NOT NULL DEFAULT 0;
CREATE TABLE watch_integrity(id TEXT PRIMARY KEY, auth_tag TEXT NOT NULL);
