# Changelog

## [Unreleased]

### Added

- Publish verified model context, output, reasoning, tool, and image-input capabilities in native and Home `/v1/models` catalogs, including local engine metadata and optional sourced operator overrides (#4469).

- Discover explicitly configured local engine catalogs in native and Home modes, with authenticated status/refresh endpoints and cancelable bounded probes (#4459).

### Fixed

- Keep local-password management authentication and routes enabled across configuration reloads (#4469 provenance preservation).
