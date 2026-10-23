CREATE TABLE filesystem_objects (
 object_key TEXT PRIMARY KEY,
 volume_id TEXT NOT NULL,
 file_id TEXT NOT NULL,
 size_bytes INTEGER NOT NULL,
 mtime_ns INTEGER NOT NULL,
 ctime_ns INTEGER NOT NULL,
 birthtime_ns INTEGER NOT NULL,
 content_hash TEXT NOT NULL CHECK(length(content_hash)=64),
 verified_at_ns INTEGER NOT NULL
);
CREATE INDEX filesystem_objects_identity ON filesystem_objects(volume_id,file_id);
CREATE INDEX filesystem_objects_expiry ON filesystem_objects(volume_id,verified_at_ns);
CREATE TABLE source_paths (
 source_id TEXT NOT NULL,
 relative_path TEXT NOT NULL,
 volume_id TEXT NOT NULL,
 file_id TEXT NOT NULL,
 file_type TEXT NOT NULL,
 size_bytes INTEGER NOT NULL,
 mtime_ns INTEGER NOT NULL,
 ctime_ns INTEGER NOT NULL,
 birthtime_ns INTEGER NOT NULL,
 object_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('selected','excluded')),
 reason TEXT NOT NULL,
 scan_generation INTEGER NOT NULL,
 entry_data TEXT NOT NULL CHECK(json_valid(entry_data)),
 PRIMARY KEY(source_id,relative_path)
);
CREATE INDEX source_paths_identity ON source_paths(volume_id,file_id);
CREATE INDEX source_paths_object ON source_paths(object_key);
CREATE INDEX source_paths_generation ON source_paths(source_id,scan_generation);
CREATE TABLE source_scans (
 source_id TEXT PRIMARY KEY,
 generation INTEGER NOT NULL,
 root_path TEXT NOT NULL,
 root_identity TEXT NOT NULL,
 volume_id TEXT NOT NULL,
 filesystem TEXT NOT NULL,
 cacheable INTEGER NOT NULL,
 policy TEXT NOT NULL,
 started_at_ns INTEGER NOT NULL,
 completed_at TEXT NOT NULL
);

CREATE TABLE scan_generations (source_id TEXT PRIMARY KEY,next_generation INTEGER NOT NULL);
