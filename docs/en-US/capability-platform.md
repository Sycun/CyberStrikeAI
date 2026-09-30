# Capability platform: identity, manifest, policy pipeline

This describes what is **implemented** (research background: `capability-platform-decoupling-research.md`).
The full inventory is generated — `docs/zh-CN/capability-catalog.md`, produced by `make generate`, never edited by hand.

## 1. The invariant

**A capability with no registered manifest cannot execute.**

Previously any non-builtin tool fell through to the coarse `agent:local-execute` permission,
and exactly the recipes that `exec()` a model-supplied string (`exec`, `angr`, `pwntools`)
lived in that fallback — prompt injection was enough to reach host code execution.
There is now one decision entry point on the execution path: the registry and evaluator in
`internal/capability`. A lookup miss is a denial, never a fallback.

## 2. Identity

`publisher.capability.name`, lowercase, dot-separated. `core.*` is reserved for the shipped binary.

The same string is used by permission declarations, approval records, revocation lists, the
generated catalog, and store submissions. `CheckTyposquat` rejects a submission within edit
distance 2 of an existing identity, so `cora.nmap` cannot be published next to `core.nmap`.

Wire-level tool names (`nmap`, `c2_task`) are unchanged; identity is an added layer, not a rename.

## 3. Class and the approval floor

| class | meaning | approval | permission |
|---|---|---|---|
| `readonly` | observes state, changes nothing | exemptible | its `:read` |
| `mutating` | changes local state or probes a target non-destructively | per session HITL | its `:write` / `agent:local-execute` |
| `destructive` | runs model-supplied code, builds payloads, dispatches implant tasks, deletes data | **always requires a human decision; no whitelist can exempt it** | a dedicated permission (`agent:destructive-execute` for recipes) |

A `destructive` capability may not use the `agent:local-execute` fallback. That is a **load-time**
failure, not a runtime convention, so a submitted artifact cannot escalate by reusing a permission
that already exists in the deployment.

The floor is released by `capability.ApprovalLedger`: single-use, 60-second expiry by default,
bound to `(conversation, capability identity)`. One human approval releases exactly one invocation.
The only writer is the HITL layer when an approver accepts
(`handler.TrackApprovedHitlExecution`); request bodies, roles, skills and store artifacts cannot
add to it.

## 4. New semantics for the no-approval whitelist

Intersection, not union. A tool is exempt only when it appears in **both**:

1. `hitl.tool_whitelist` in `config.yaml` (operator-owned; writing it requires `config:write`, i.e. admin), and
2. the tool set submitted for the session.

A session request body can now only *narrow* exemptions. `destructive` capabilities are never
exempt, even when both lists name them.

## 5. Adding a tool: zero Go files for recipes

Add a `capability:` block to `tools/<name>.yaml`:

```yaml
capability:
  id: "core.nmap"                 # or <publisher>.<name>
  version: "1.0.0"
  class: "mutating"               # readonly | mutating | destructive
  permission: "agent:local-execute"
  approval: "inherited"           # never (readonly only) | inherited | always
  runtime: "recipe:exec"
  grants:                         # a ceiling on mediated side effects, not a request
    - "process.exec(nmap)"
    - "net.connect(target)"
  evidence: false
  timeout_seconds: 0
```

Constraints enforced at load time (the tool stays visible but unexecutable when violated):

- `class: destructive` ⇒ `approval` may not be `never`, and `permission` may not be `agent:local-execute`
- `class: readonly` ⇒ may not set `approval: always`
- `runtime: recipe:exec` ⇒ at least one `grants` entry
- `id` must be a valid lowercase dotted identity

A built-in Go tool needs one row in `internal/capability/policy_builtin.go`. Names stay in
`internal/mcp/builtin/constants.go`; `TestDeclaredConstantNamesHavePolicies` parses that file's string
literals and asserts both directions agree, so a fourth hand-maintained list cannot appear.

## 6. One manifest, several downstream artifacts

`make generate` builds from the policy table plus the recipe manifests:

- `web/static/js/generated/capability-catalog.js` — `window.CSAI.capabilities`,
  `capabilityNames`, `capabilityByName` (replaces hand-mirrored frontend tool-name enums)
- `web/static/js/generated/capability-catalog.json` — same data, machine-readable
- `internal/capability/testdata/catalog.golden.json` — drift baseline
- `docs/zh-CN/capability-catalog.md` — human-readable inventory for reviewers

`TestGeneratedCatalogIsUpToDate` plus the CI regenerate-and-diff step fail when a manifest changed
and the artifacts did not.

Argument JSON Schema is generated from the same `parameters:` list (`capability.JSONSchema`) and
drives `capability.ValidateArgs`: a missing required argument now produces an argument error, not a
permission error.

## 7. Assembly point

There is one. `internal/app.InstallCapabilityRegistry` assembles at startup; `cmd/mcp-stdio` must call
`app.InstallStdioPolicy` to reuse the same pipeline, with its identity coming from the `mcp_stdio`
section of `config.yaml`. stdio has no human approval channel, so `destructive` capabilities cannot
execute there; with no `mcp_stdio.permissions` declared the process refuses every tool call.

`mcp.Server.SetRequestContextDecorator` is how an entry point binds identity, so no entry point needs
its own assembly code.

## 8. Plugin runtime

`internal/pluginhost` is where a `runtime: plugin-host:*` capability executes.

- ABI: line-framed JSON-RPC, version constant `csai-plugin/1`. The host calls
  `initialize` / `capabilities/list` / `capabilities/invoke` / `shutdown`; a plugin may
  only call back into `host/grant_check` / `host/log` / `host/progress`, everything else
  is method-not-found. Reference implementation: `internal/pluginhost/testdata/refplugin`.
- Isolation: one child process per trust domain (the publisher namespace), lazy start,
  restart on the next call after a crash, idle reaping, under `processguard` cgroup/rlimit.
- Credentials: the environment is inherited from an allowlist, and any key whose name
  contains `KEY`, `TOKEN`, `SECRET` or `PASSWORD` is refused outright.
- Egress: the child only receives the host-side CONNECT proxy address. What actually gets
  allowed is **manifest `grants` ∩ the operator-approved `(host, ports, method, valid_minutes)`
  tuples**, and an empty approved set denies everything. Neither a store artifact nor the
  plugin can widen that set.
- Unconfigured means refused: with `plugin_host.enabled: false`, a capability declaring a
  plugin runtime fails at execution rather than falling back to running the recipe in-process.

Limitation worth stating: the proxy governs traffic that uses it. Go deliberately never
proxies loopback and raw sockets bypass it, so this is not an OS-level network boundary.
A hard boundary needs a per-instance network namespace, which `processguard` does not
provide. This repository also does not bundle a Python runtime - `plugin-host:python`
expects an interpreter and plugin binary to be supplied.

See the `plugin_host` section of `config.example.yaml`.

## 9. Artifact trust, revocation and the capability-delta gate

`internal/artifact` implements the decision and client-enforcement half of a store; the
registry service itself is not built.

- **The signature covers every security field**: `Manifest.CanonicalBytes()` includes
  id/version/class/permission/approval/runtime/grants/payload digests/publisher, so editing
  a class or adding a grant after approval invalidates the signature.
- **There is no `ignoreUnverified`**: unsigned, unknown-publisher and key-mismatch cases are
  all refused. Executable artifacts must be readable (`File.Text`) - deliberately not copying
  the reference product's plugin encryption, because code that reaches operator credentials has
  to be auditable.
- **Revocation applies at two moments**: loaded at startup, then re-checked by an evaluator
  stage before every call. A hit isolates the capability from the registry so the model cannot
  even see it; publisher-level revocation covers every artifact from that publisher. `Merge`
  never un-revokes, and a malformed list is an error rather than a silently empty one.
- **Provenance is stamped by the installer** after verification - there is no digest field a
  recipe can fill in - and the ledger lives outside the registry, so applying config cannot
  erase the basis for a revocation match.
- **Capability-delta gate**: against the approved baseline, any new grant, class escalation or
  permission change requires an independent second reviewer, and the author may not review
  their own submission. An identical manifest is auto-rescanned; narrowing capabilities does
  not trigger re-review, so human time goes to increases.
- **Static scan** blocks instruction overrides, exfiltration requests, private-key/token
  material, `curl | sh`, `` !`cmd` `` dynamic context, role-tag smuggling and control characters.
- **`SanitizeForIndex`** strips URLs, IPs and CIDRs before embedding.
- **Quarantine** moves content aside and keeps it visible for triage instead of deleting it.

## 10. Content privilege hierarchy (community knowledge, personas, prompts)

`internal/contentpolicy` implements the code-level controls from research section 6. The
reason is direct: retrieved knowledge enters the context of an agent that can operate a real
C2 and webshell, and published results show a small number of poisoned documents is enough.
So this is structure, not a wording request:

- **Tag**: `[[csai:untrusted-advisory]]`, written together with a fixed preamble.
- **One tagged exit**: `RetrievalResult.AdvisoryContent()` is the only way chunk text reaches
  model view; both the MCP tool result and the Eino retriever exit go through it, and a
  source-scan test forbids any unfenced write. Fencing is idempotent.
- **Assembly guard**: `newEinoAgenticChatModelAgent` calls `GuardDecisionPath` on the
  `Instruction`, so tagged content in an operator-controlled channel refuses to build the agent.
- **Rendering never executes**: bang-backtick interpolation, `![x](url)` transclusion, role tags,
  control characters and block terminators are stripped.
- **Poisoning stopped at ingest**: `RefuseIngest` rejects instruction-shaped text before the
  knowledge item is written.
- **Index sanitisation**: `StripForIndex` removes URLs, IPs and CIDRs.

Trade-off stated explicitly: role, skill and markdown-agent text is treated as approved,
installed, operator-consented configuration rather than a runtime-untrusted injection surface;
community knowledge text is the unbounded surface. CaMeL-style dual-model separation is not
implemented.

## 11. Provider dialect layer

`internal/provider` holds the only vendor decision data. **A dialect is not a vendor**:
there are three wire families (`openai-chat`, `openai-responses`, `anthropic-messages`) and
vendors are rows in a table.

- Each `Dialect` carries `API / BaseURL / DefaultBaseURL / ContextWindow / Cost / Capabilities / Retry / OverflowMarkers / AliasesTo`.
- `Resolve(vendor, baseURL)` degrades an unknown name to the chat dialect, so a new gateway
  name in a deployment cannot stop the service from starting.
- Call sites no longer compare provider strings. They ask `AgenticBackendSupported`,
  `IsAnthropicMessagesVendor`, `EffectiveProviderName`, `DefaultBaseURLFor`, `ListsModels`,
  and `ClassifyError(status, body)`.
- **Adding a vendor is a table row** (`Catalog.Register`), not a code change at any call site.
- The consistency suite is this layer's acceptance gate and the only objective basis for
  deciding whether two near-duplicate implementations can be merged: the five scenarios
  (abort, context overflow, tool-call-without-result, unicode surrogates, cross-provider
  handoff) run against **every row** of the catalog.

Rerank provider names are a **separate namespace**; do not fold them into the model dialect
table (noted in `config.go`).

## 12. Not implemented yet

- P6 remainder: per-domain Store extraction (`internal/store` already owns notification reads, the
  `hitl_interrupts` surface the HTTP layer uses, the shared conversation-visibility clause, and the
  `messages` writes - which were one UPDATE copied to 15 sites across six files plus a twin CASE
  append differing by a single clause; the digest's two remaining cross-domain reads now live in
  `store.Vulnerability` and `store.Execution`, so the transport layer assembles **no SQL at all**
  (it started at 49), and the HITL/session/notification-read tables are pinned to a single writer
  by a repository-wide ownership test; **the narrow interfaces are no longer paper contracts** -
  fifteen domains now hold their own store interface as the field type, and `internal/handler` is down
  from 19 structs holding `*database.DB` to 3 (per-file ceilings plus an only-up narrowed-store floor,
  in `make layering-check`). Every one of those fields is assigned through `database.Narrow`, which is
  load-bearing rather than cosmetic: `var store AssetStore = (*DB)(nil)` is a *non-nil* interface, so a
  plain assignment would permanently invert all 64 `if h.db == nil` degradation guards in the transport
  layer - and that compiles, with every enabled-path test still green. This was not argued from theory:
  my first substitution matched only the single-spaced `db: db,` and missed five aligned assignments,
  and `TestRobotModeRejectsUnavailableMultiAgent` panicked on `(*DB).GetRobotSessionBinding` with a nil
  receiver. The 3 remaining structs are blocked for a measured reason rather than queue order - their
  `h.db` escapes into another package's signature (`multiagent.RunDeepAgent` /
  `RunEinoSingleChatModelAgent`, `agentfinalizer.FromRunResult`, six calls into `internal/project` and
  `internal/attackchain`, `workflowrunner.RunArgs.DB`) - so those functions need consumer interfaces
  first. That layer has now been crossed four times: `conversation.go`'s two shared history renderers
  needed exactly one method; `audit.go`'s single escape `audit.ApplyResourceAvailability` needed eight
  existence lookups and now declares `audit.ResourceExistenceSource`; `monitor.go`'s four escapes were
  two local helpers needing one method each; and `attackchain.go`'s `attackchain.NewBuilder(h.db, …)`
  became `attackchain.Store` (nine methods), which also stops *that package* from holding the
  361-method object. Each time the handler's own interface was widened to a superset, because a
  generated-from-direct-calls surface always misses the escaped receiver. Two counter-examples are
  worth as much: `batch_task_manager.go` has 22 methods on `m.db` and zero escapes, so swapping its
  field type compiled first try - the interface had been generated from its real surface - and
  `knowledge.go`'s field was **never read at all**, so the honest fix was to delete the field and the
  constructor parameter rather than invent a store for a dead dependency),
  event-sourced sessions, and `AgentHandler` decomposition -
  which is now measured and gated instead of being a hunch (the provider catalog is generated and
  byte-gated: `internal/provider/publish.go` renders it into `docs/zh-CN/provider-catalog.md` plus
  `internal/provider/testdata/provider-catalog.golden.json`, and CI fails on drift):
  `AgentHandler` holds **130 methods
  across 23 files**, and `internal/handler` as a whole declares **64 `Set*` injection methods over
  21 receiver types**, 18 of which are byte-identical copies of `SetAudit` (the report's "26 SetXxx
  / 19 files" underestimated both). Three only-down gates cover it (`make layering-check`: per-type
  method ceilings, a file ceiling, and a whole-package setter ceiling), one cohesion collapse has
  landed (the three HITL config savers became one collaborator and one setter), and the real risk in
  those 18 copies is now closed by an **audit-injection completeness gate**: every injection goes
  through `bindAudit`, and `TestEveryAuditableHandlerIsAuditBound` derives the required set by
  parsing the handler package, so a new auditable handler that is constructed but never bound fails
  CI. The consequence of a missed injection is exactly the kind that never shows up as an error -
  the endpoint keeps serving and writes no audit records at all. The 18 `SetAudit` methods were
  deliberately *not* merged into one embedded collaborator: it would flip the meaning of 89
  hand-written `if h.audit != nil` guards, and neither the compiler nor the existing tests report
  that kind of inversion. Catalog
  codegen, and merging the twin function pairs (the dialect consistency suite is now the objective
  judge for that). Converging Eino into one adapter package is now gated rather than merely
  intended: `internal/layering` pins a per-package file count outside the adapter packages (a
  brand-new importer fails outright, growth inside a debt package fails with the file list,
  shrinkage only asks to tighten the baseline), `make layering-check` runs in CI, and the water
  mark is now 6 importing packages down from 11, with the debt surface at 3 packages / 96 files
  from 8 / 100 while the total Eino-importing file count stays exactly 104 - the code moved into
  the boundary packages rather than being deleted or grown. Four slices so far: `internal/vision`'s
  model call behind `llm.DescribeImage`, the Claude connection probe behind `llm.PingAgentic`, all
  of `internal/reasoning`'s rules behind its own `ChatModelTarget` interface with the SDK mapping
  in `internal/llm/reasoning_target.go`, and `internal/security`'s streaming shell behind its own
  `ShellEvent`/`ShellSink` with the ADK shim next to the wrapper that already existed. What is left
  (`multiagent`, `knowledge`, `workflow`) hosts the Eino orchestration itself, so shrinking it needs
  interface-ization rather than another move.
- P3 remainder: per-file ES modules (six giant scripts still duplicate their own helper copies,
  2,800-3,200 lines to dedupe); the persisted `process_details.eventType` names the page rebuilds
  its timeline from (35 of them) are now compared against a server-side inventory too; that
  inventory is precise for direct writes and local assignments but not for rows written through a
  progress-callback variable, so the "persisted yet unrendered" direction can under-report until
  the value graph learns that a callback variable holds a function body; and `POST /api/terminal/run/stream` has **no frontend consumer at all**
  (the terminal pane uses the WebSocket) - keeping that endpoint is an open decision.
  Done: the frame-level `type`, the event names carried inside progress callbacks (63 in total,
  49 of which are only provable from the callback's call sites), and the C2 stream's `category` -
  all three wire formats now come out of `internal/sse` with zero hand-assembled frames. The
  generated enum is loaded by `index.html` and checked per frame through `CSAI.isSSEEvent`, and the
  two-sided diff is pinned by only-go-down ratchets (2 events the page does not render, 1 dead
  branch in the page).
- The registry service: keyless signing at publish, staged rollout, release-age cooldowns.
- Air-gapped offline bundle export/import; automated sandbox detonation (gate 2 is recorded and
  required, not yet executed by the pipeline).
- netns/seccomp enforcement of `grants`; distribution of an embedded CPython.
- Deciding whether role, skill and markdown-agent text should also be runtime-untrusted.
- CaMeL-style dual-model control-flow separation (still paper-grade).
