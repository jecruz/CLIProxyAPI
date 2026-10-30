# Changelog

## [Unreleased]

### Added

- Resolve provider Keychain references from a consumed per-launch memory map;
  fail closed and exclude resolved credentials from config, persistence, export
  and account/log metadata (#4565).

- Publish verified model context, output, reasoning, tool, and image-input capabilities in native and Home `/v1/models` catalogs, including local engine metadata and optional sourced operator overrides (#4469).

- Discover explicitly configured local engine catalogs in native and Home modes, with authenticated status/refresh endpoints and cancelable bounded probes (#4459).

### Fixed

- Resolve all provider-doctor Keychain references before probing, refusing mixed
  providers with any unavailable reference without an upstream request (#4578).

- Preserve local discovery through management GET/PUT/PATCH, retain healthy catalogs for routing-only edits, and document opt-in discovery (#4459).
- Omit ambiguous Home route capabilities and require route-specific metadata or sourced overrides for configured aliases (#4469).

- Keep local-password management authentication and routes enabled across configuration reloads (#4469 provenance preservation).
