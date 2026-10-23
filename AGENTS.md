# LocalBackup engineering rules
- React never constructs or executes shell commands. All file access is through the authenticated Go API.
- Resolve and validate paths, source/target overlap, repository and volume identity before writes.
- Never mutate a committed snapshot. Publish staging only after every enabled source verifies.
- Never follow source symlinks. Restore only into a new path outside sources and repositories.
- Backup operations must be previewable, auditable and cancellable.
- Retention requires an explicit preview and confirmation; always preserve the latest snapshot.
- Schema changes use numbered SQL migrations. Add meaningful safety/integration tests for features.

# Brand, compatibility, and public releases
- Product name: Foldsnap · 留档. Preserve the legacy LocalBackup bundle identifier, data/log directories, repository namespace and stored bindings; branding must not migrate user data.
- Use isolated test profiles and generated fixtures. Do not start real automatic backup plans merely to validate an app update.
- Change versions only in the application version fields. Never globally replace version strings in package-lock.json or rewrite dependency versions/engine requirements during an app version bump.
- Keep runtime databases, access tokens, fingerprints, personal paths and local diagnostics out of Git. Public commit metadata should use the GitHub noreply address.
- A public release must pass a clean dependency install and macOS build in CI, alongside the safety/integration checks.
