# LLM Dynamix Engine Backend Source

This fork's `llmdynamix/main` branch maintains the Go backend for the LLM Dynamix
macOS app. App builds vendor a pinned commit into `backend/llmdynamix-engine`;
make engine changes here, then update `backend/llmdynamix-engine.pin` and run
`scripts/sync-backend-source.sh` in the app repository. That sync replaces the
vendored directory completely; it has no patch or overlay mechanism.

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

OpenAI-compatible keys may be `keychain://caiss/providers/NAME` references
(ASCII account names: letter/underscore, then up to 127 letters/digits/`_.-`).
The app supplies `LLMDYNAMIX_PROVIDER_KEYS_JSON` only in the child environment;
the engine consumes/unsets it before plugin startup. Resolved keys exist only
in runtime-only auths: config retains references, persistence/export is blocked,
and account/log metadata omits key material. Missing or malformed references
disable the entire affected provider, including mixed literal/reference keys.
Rotation requires a fresh app-owned backend launch. No migration is performed.

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

## Model capabilities

Standard `GET /v1/models` responses retain the OpenAI list envelope and identity
fields, and add only known `context_length`, `max_output_tokens`,
`supports_reasoning`, `reasoning_levels`, `supports_tool_calls`, and
`supports_image_input` fields. Explicit `false` is retained; unknown values are
omitted. Duplicate Home IDs shared by multiple routes omit capability claims
until a sourced operator override resolves them. Limits come from registry metadata or the upstream catalog for the
actual route. Home catalogs accept the OpenRouter `top_provider` and
`architecture` metadata shapes. Opt-in local discovery also preserves capability
metadata reported by the local engine. No extra inference request is made, and
this feature does not fetch additional remote provider catalogs.

The compatibility provider's internal default reasoning levels remain available
for request routing but are never advertised as verified model capabilities.
No Sol or Terra limits are built in. Configured aliases do not borrow static
capabilities from other routes; use route-reported metadata or sourced overrides. An OpenRouter route's limits do not prove
the capabilities of an OpenAI or Codex OAuth route with a similar name.

The optional `model-capabilities` configuration map defaults to empty. Each key
must match the exact published model ID. Each override requires a nonempty
`source` describing the operator's route-specific evidence; an entry without
one is ignored. Overrides affect catalog metadata, not routing or token limits
sent to inference. Omitted fields preserve upstream metadata. For YAML, the
optional fields are `context-length`, `max-output-tokens`, `supports-reasoning`,
`reasoning-levels`, `supports-tool-calls`, and `supports-image-input`.

```yaml
model-capabilities: {}
# Add entries only after verifying the actual provider route, for example:
#   exact-published-model-id:
#     source: "Provider documentation URL or dated operator verification"
#     supports-tool-calls: true
```

A sourced override adds `capability_source` to the corresponding public model
record. No source text or capability values are invented by the engine. Test
fixtures with synthetic Sol/Terra limits exercise propagation only; they are not
real route specifications.

Run focused tests with `go test ./internal/registry ./internal/localdiscovery ./internal/config ./sdk/api/handlers/openai ./internal/api ./sdk/cliproxy`.
