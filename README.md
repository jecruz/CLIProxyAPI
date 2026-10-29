# LLM Dynamix Engine Backend Source

This directory contains the vendored Go backend used to build `llmdynamix-engine`
for the LLM Dynamix macOS app.

The source is pinned by `../CLIProxyAPI.pin` and refreshed by the repository-owned
sync script at `../../scripts/sync-backend-source.sh`. Production app builds must
use `../../scripts/build-backend-engine.sh`; do not build the shipped engine from
a sibling checkout.

## Build

From the LLM Dynamix repository root:

```bash
TARGET_ARCH=arm64 ./scripts/build-backend-engine.sh
TARGET_ARCH=x86_64 ./scripts/build-backend-engine.sh
```

The output is written to:

```text
src/Sources/Resources/llmdynamix-engine
```

## Runtime Defaults

- Product name in runtime output: `LLM Dynamix Engine`
- Default backend port: `12445`
- Default auth/config directory: `~/.llmdynamix`
- Optional management release metadata repository: `jecruz/llmdynamix`
- Bundled app serving path: `LLM Dynamix.app/Contents/Resources/static`

## Compatibility Boundary

The Go module path and internal import paths intentionally remain compatible with
the upstream module recorded in `../CLIProxyAPI.pin`. Do not rename Go imports as
part of product rebranding unless the full module move is planned and tested.

Developer examples and Docker files in this directory should use the LLM Dynamix
Engine binary name, container names, ports, and runtime directories.
