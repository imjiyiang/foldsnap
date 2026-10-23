# LocalBackup engineering rules
- React never constructs or executes shell commands. All file access is through the authenticated Go API.
- Resolve and validate paths, source/target overlap, repository and volume identity before writes.
- Never mutate a committed snapshot. Publish staging only after every enabled source verifies.
- Never follow source symlinks. Restore only into a new path outside sources and repositories.
- Backup operations must be previewable, auditable and cancellable.
- Retention requires an explicit preview and confirmation; always preserve the latest snapshot.
- Schema changes use numbered SQL migrations. Add meaningful safety/integration tests for features.
