CREATE TABLE run_jobs (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 run_id TEXT NOT NULL UNIQUE REFERENCES backup_runs(id) ON DELETE CASCADE,
 plan_data TEXT NOT NULL CHECK(json_valid(plan_data)),
 approval_data TEXT CHECK(approval_data IS NULL OR json_valid(approval_data)),
 state TEXT NOT NULL CHECK(state IN ('queued','running','held')),
 not_before_ns INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX run_jobs_ready ON run_jobs(state,not_before_ns,sequence);
