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

## 12. Capability units and hot-plug

Five kinds of extension - roles, skills, markdown agents, tool recipes, MCP declarations - each
used to have its own lifecycle, and **none of them could change without a restart**. They now share
one identity scheme and one live table:

- `internal/plugin`: a `Unit` (identity `<kind>/<name>` plus source path plus install-time digest)
  and a `Bundle` (a set of units installed and removed together). Readers get an **immutable
  snapshot behind an atomic pointer** (lock-free); one mutex serialises writers only.
- Conflicts **refuse and name the owner** (`*ErrConflict`) instead of overwriting: a bundle cannot
  shadow a shipped capability, a directory scan cannot shadow an installed bundle, re-installing the
  same id is an upgrade that reclaims only its own previous units, and unplugging detaches without
  deleting any file.
- `bundles/<id>/bundle.yaml` is the shape of **packaging by role** (role + sub-agent + skills +
  tools); paths are confined to the bundle directory by `skillpackage.SafeRelPath` and `version` is
  mandatory, because a pack without one cannot be upgraded or rolled back. Format and ownership
  rules: `bundles/README.md`; worked example: `bundles/mobile-app-security`.
- **Shipped capabilities go through the same table**: `roles/ agents/ skills/ tools/` are scanned
  into units whose identities match the existing loaders entry for entry (measured 142: 13 roles /
  16 agents / 23 skills / 90 tools), pinned by `internal/app/plugin_parity_test.go` - the truth
  source is those loaders, not a hand-written list.
- The safety of hot-swap is **demonstrated, not argued**: replacing the copy-on-write clone with an
  in-place write makes `TestConcurrentReadersNeverTear` report the write-vs-iterate race under
  `-race`. That in-place pattern is precisely what the role API used to do, including allocating
  the map inside a GET.
- Roles are wired through to the run path: a write is "file -> unit -> publish a new snapshot" and
  a read is `currentRoles(h.config)` (`internal/handler/live_config.go`). If assembly forgets to
  install the live store, `make wiring-check` fails - and that omission **compiles cleanly with the
  enabled-path tests green**, which is why it has to be a gate.
- Skills are wired through too, and only after **replacing a vendor implementation**: Eino's own
  backend accepts one `BaseDir`, so a skill inside a bundle was structurally unreachable.
  `internal/einoskill` implements that two-method backend over the capability table instead (no
  symlink farm, no copying of somebody else's files), and `internal/multiagent` prefers it whenever
  a table is installed. Swapping a vendor component is guarded by comparing against the vendor as
  the truth source - `TestBackendMatchesEinoBackend` checks front matter, body and base directory
  for all 23 shipped skills - and `TestTabInBodyIsNotStripped` blocks the tempting copy of the
  vendor's `stripLineNumbers`, which exists only because *its* local backend prefixes lines with
  `N\t`; applying it to bytes read straight from disk truncates every real tab. Agents and tools
  still re-scan their directories per run; `bundles/README.md` states the gap per kind.

## 13. Not implemented yet

- P6 remainder: per-domain Store extraction (`internal/store` already owns notification reads, the
  `hitl_interrupts` surface the HTTP layer uses, the shared conversation-visibility clause, and the
  `messages` writes - which were one UPDATE copied to 15 sites across six files plus a twin CASE
  append differing by a single clause; the digest's two remaining cross-domain reads now live in
  `store.Vulnerability` and `store.Execution`, so the transport layer assembles **no SQL at all**
  (it started at 49), and the HITL/session/notification-read tables are pinned to a single writer
  by a repository-wide ownership test; **the narrow interfaces are no longer a paper contract - they
  are this layer's hard invariant** - eighteen domains now hold their own store interface as the field
  type, one dead field was deleted outright (`KnowledgeHandler.db`, never read: the honest fix was to
  drop the field and its constructor parameter rather than invent a store for it), and `internal/handler`
  went from 19 structs holding `*database.DB` to **zero** (zero `*sql.DB` fields as well). The gate
  flipped from a ratchet to `TestHandlerLayerHoldsNoGodObject`, which fails on any single occurrence and
  guards its own emptiness by requiring the walk to have seen ~990 struct fields, plus a shape gate
  (`TestNarrowedFieldsAreOnlyAssignedThroughNarrow`) asserting every assignment to a narrowed field goes
  through `database.Narrow`. Both run in `make layering-check`. Every one of those fields is assigned through `database.Narrow`, which is
  load-bearing rather than cosmetic: `var store AssetStore = (*DB)(nil)` is a *non-nil* interface, so a
  plain assignment would permanently invert all 64 `if h.db == nil` degradation guards in the transport
  layer - and that compiles, with every enabled-path test still green. This was not argued from theory:
  my first substitution matched only the single-spaced `db: db,` and missed five aligned assignments,
  and `TestRobotModeRejectsUnavailableMultiAgent` panicked on `(*DB).GetRobotSessionBinding` with a nil
  receiver. The last three structs fell to declaring the interface at the far end of each chain rather
  than faking one in the handler: `multiagent` turns out to call **no** database method itself and only
  forwards the handle to `internal/project`, so the surface belongs to `project` (13 methods - the
  project row plus the fact and fact-edge ledger); `agentfinalizer` needs two (read/save one tool
  execution); `attackchain` needs the chain rows, the conversation evidence, and the fact ledger its
  promotion path writes; the workflow engine needs its own run/node-run ledger plus project facts.
  Surfaces more than one package needs are declared once in `internal/database/surfaces.go` (every
  consumer imports database, so declaring them consumer-side would create a cycle) and aliased back as
  `project.Store`, `agentfinalizer.Store`, `attackchain.Store` - one method list, one
  `var _ X = (*DB)(nil)` assertion each, instead of copying thirteen signatures. `Close` is
  deliberately absent from all of them: a consumer of the shared handle must not be able to shut it
  down. The shape gate's own delivery story is worth keeping: its first version passed a probe it
  should have failed, because it rebuilt a module-relative path by re-prepending `internal/handler` -
  the file never opened, `continue` swallowed it, and an empty set read as "no violations". Every
  branch of a text-scoped gate now has to prove it saw something
  (`declares a narrowed storage field but no assignment to it was found`), and a second false
  positive - `if h.db == nil` guards parsed as assignments, since RE2 has no negative lookahead -
  had to be excluded explicitly. Two rules earned: **run both probes** (inject a violation, expect
  red; clean tree, expect green) for every new gate, and **never let a text judgement succeed on an
  empty set**. One counter-example argues the process worked: `batch_task_manager.go` has 22 methods on
  `m.db` and zero escapes, so swapping its field type compiled first try - that interface had been
  generated from its real surface all along),
  event-sourced sessions, and `AgentHandler` decomposition -
  which is now measured and gated instead of being a hunch (the provider catalog is generated and
  byte-gated: `internal/provider/publish.go` renders it into `docs/zh-CN/provider-catalog.md` plus
  `internal/provider/testdata/provider-catalog.golden.json`, and CI fails on drift):
  `AgentHandler` started at **130 methods
  across 23 files**; two cuts have since landed - 9 interrupt-queue read methods into `HITLQueue` and
  10 finalization methods into `runFinalizer` - so the ceiling is now **112 methods / 21 files**, and
  `internal/handler` as a whole declares **64 `Set*` injection methods over
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
  from 8 / 100 while the total Eino-importing file count went 104 -> 105. Earlier slices were pure
  moves - code relocated into the boundary packages, nothing deleted, nothing grown - and the +1 is
  the one genuine addition: `internal/einoskill`, which exists because Eino's own skill backend
  accepts a single `BaseDir` and therefore cannot reach a skill that lives inside a bundle (see
  section 12). The debt surface - the side the acceptance criterion actually watches - did not
  regress. The code moved into
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
