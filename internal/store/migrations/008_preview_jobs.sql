CREATE TABLE preview_jobs(id TEXT PRIMARY KEY, data TEXT NOT NULL CHECK(json_valid(data)));
