# Developer Guide

[中文](../zh-CN/developer-guide.md)

This guide is for contributors extending CyberStrikeAI. The project is a Go single-service application with a static frontend, SQLite persistence, Agent/MCP orchestration, and optional high-risk security subsystems.

## Project Layout

```text
cmd/server/              service entrypoint
internal/app/            app wiring, routes, MCP tool registration, capability policy assembly (the only assembly point)
internal/capability/     capability identity, manifest, authorization and approval floor (leaf package; depends on no business package)
internal/handler/        HTTP handlers
internal/database/       SQLite access (legacy surface; new domains go to internal/store)
internal/store/          per-domain persistence: one file per domain, takes only *sql.DB, testable against a real database
internal/security/       auth, rate limits, shell execution
internal/mcp/            MCP server and external MCP manager
internal/multiagent/     Eino single-agent, multi-agent, middleware
internal/workflow/       graph orchestration runtime
internal/knowledge/      indexing and retrieval
internal/c2/             built-in C2
internal/project/        project fact blackboard
web/static/              frontend JS/CSS/assets
web/templates/           HTML templates
tools/                   YAML command tools
roles/                   role YAML
agents/                  multi-agent Markdown definitions
skills/                  Agent Skills
docs/                    documentation
```

## Development Startup

```bash
go run ./cmd/server --config config.yaml
```

The frontend is static. Most JS/CSS/template changes only require a browser refresh.

## Development Tree and Test Tree

The development tree holds source only. **Building, running the gates and doing live
verification all happen in a test tree** (by default `~/csai-测试版`, a clone of this one), so a
compiled binary, or the `config.yaml` / `data/` / `log/` a running instance writes, can never end
up next to the refactor and get committed by accident.

One-time setup:

```bash
git clone --single-branch --branch main <path-to-dev-tree> ~/csai-测试版
cp ~/csai-测试版/config.example.yaml ~/csai-测试版/config.yaml  # give it its own ports
```

The four daily commands (thin wrappers around `scripts/testtree.sh`):

| command | what it does |
|---|---|
| `make test-sync` | pushes the dev tree's source into the test tree, and deletes code files the dev tree no longer has |
| `make test-verify` | compares both trees file by file (digests) and exits non-zero listing what differs |
| `make test-gates` | sync + verify, then runs `fmt-check vet layering-check wiring-check js-check test-race` and both builds **in the test tree** |
| `make test-run` | sync + verify, then builds and starts the server in the test tree |

Parity is measured on **content**, not on git HEAD, because a debugging change is usually not
committed yet while the rule is "both trees change together". Editing a code file inside the test
tree makes `make test-verify` fail and name that file; go back to the dev tree, edit there, then
`make test-sync`. The test tree's `config.yaml` / `data/` / `log/` are ignored runtime files - never
compared, never deleted - and a bundle dropped into its `bundles/` on purpose is only reported, not
removed, because that is verification input rather than source. Override the location with
`CSAI_TESTTREE` or `make test-gates TESTTREE=...`.

## Adding a Business Module

Do not add only a handler. A complete module usually needs:

1. Data model and SQLite migration. New domains live in `internal/store/` (one owner per table,
   handlers depend on a consumer-side interface); only existing domains still grow in
   `internal/database/`. The HTTP layer must not contain raw SQL — `TestHandlerRawSQLRatchet`
   ratchets `h.db` and `m.db` separately and only allows the counts down.
2. Handler: parameters, errors, pagination/filtering.
3. Audit: management actions.
4. Monitor: long-running execution state.
5. MCP: whether Agents should call it.
6. HITL: approval boundary for MCP tools.
7. OpenAPI: update `/api/openapi/spec`.
8. Frontend: i18n, states, empty/error UI.
9. Tests: DB, handler, edge cases.
10. Docs: config, usage, troubleshooting, safety impact.

Missing one of these usually becomes a later usability or safety bug.

## Error Response Design

Prefer stable JSON:

```json
{
  "error": "machine_readable_code",
  "message": "human-readable explanation"
}
```

Frontend needs stable fields, users need actionable messages, and logs need detailed internal errors.

## Long-Running Tasks

For scanning, indexing, batch tasks, C2, or external operations, answer:

- Can it be cancelled?
- Can progress be queried?
- Can it be retried?
- Where is the result stored?
- Does state survive page refresh?
- Does it block the HTTP request?

If not, use task tables, event streams, or monitoring.

## Extending Tools

Prefer `tools/*.yaml` for command tools. Use Go built-in tools when the tool needs internal state or structured integration.

Built-in tools should define clear input schemas, handle timeouts and errors, and respect HITL for risky actions.

## Frontend Changes

Use existing helpers such as `apiFetch`, modal utilities, notifications, and i18n. Update both `web/static/i18n/zh-CN.json` and `web/static/i18n/en-US.json` for new visible text.

Avoid putting secrets or provider keys in frontend code.

## Test Priority

High-value tests:

- config hot-apply;
- HITL branches;
- shell timeout/no-output;
- external MCP recovery;
- KB indexing and post-processing;
- WebShell OS/encoding detection;
- SQLite migration compatibility.

## Source Anchors

- App wiring: `internal/app/app.go`; per-domain registrars in `internal/app/routes_<domain>.go`.
  `setupRoutes` only builds groups, attaches middleware and calls the registrars - the old rule that
  every route live in one function is gone (that function was 558 lines with 30 positional params).
  `TestRouteTableMatchesGolden` asserts the served paths against `testdata/routes.golden.txt` (278 routes),
  so moving a registration cannot silently change the surface. Regenerate deliberately:
  `CSAI_WRITE_ROUTE_GOLDEN=1 go test ./internal/routes -run TestWriteGolden`.
- Capability policy: `internal/capability/policy_builtin.go` (built-in tools) or the `capability:` block in `tools/<name>.yaml` (recipes). A tool with no registered manifest is refused at execution; there is no coarse fallback. Built-ins need no Go change when they are recipes, and `make generate` refreshes the derived catalog, frontend enum and argument schemas.
- Config apply: `internal/handler/config.go`
- OpenAPI: `internal/handler/openapi.go`
- Tool executor: `internal/security/executor.go`
- Skill package: `internal/skillpackage/`
