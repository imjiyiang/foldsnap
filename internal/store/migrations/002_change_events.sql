CREATE TABLE watch_cursors(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
CREATE TABLE change_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 plan_id TEXT NOT NULL,
 source_id TEXT NOT NULL,
 relative_path TEXT NOT NULL,
 event_id TEXT NOT NULL,
 flags INTEGER NOT NULL,
 requires_scan INTEGER NOT NULL,
 changed_at_ns INTEGER NOT NULL,
 UNIQUE(plan_id, source_id, relative_path)
);
CREATE INDEX idx_change_events_plan ON change_events(plan_id,id);
