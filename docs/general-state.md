# General run state

`internal/state` owns sequential, in-memory state across inference cycles. It
contains runtime facts, task records, knowledge partitions, and an append-only
journal. The existing `loop.State` remains a temporary provider working set.
No storage/recovery engine, domain Go model, MCP infrastructure, or task planner
is introduced. Optional HTTP host work uses the same tasks and event path.

Run the CLI with zero knowledge partitions (the default), or load explicit modules:

```sh
go run ./cmd/grodt --state-definition internal/state/wasm/testdata/state.json "Respond with OK."
```

The JSON definition lists `name`, `schema`, `initial`, `module`, and `abi` for each
partition. File paths resolve relative to the definition file. Module code and
schemas are local resources; no network resolution is introduced. Every default
must validate and every module must compile, instantiate, and pass ABI checks
before initialization succeeds. A bad binary/export/version is a configuration
failure, not a stale partition that the model is expected to repair.

## Ownership and tasks

The host generates run identity, start time, event IDs, task IDs, and counters.
`state.Options` permits a supplied run identity and clock for deterministic tests
and future restoration, but there is no restart/recovery implementation. Snapshots
and journal reads return copies; state modules receive copies of only their own
domain value and an event.

Task records form a tree through canonical `parent_id` relationships. A parent
can have multiple children. `tasks.stack` is only the currently active lineage;
completed children remain in `tasks.records`. `Push` is a host operation (also available through the validated planning tool),
`Complete` pops the active task and resumes its parent, and `End` terminates the
remaining lineage and pending work. Every proposed task-state document passes the existing JSON
Schema validator before commit. The host assigns task IDs, execution status, timing, and cycle counters.
The actor proposes separate semantic plan and step statuses.

The CLI creates one root from the run objective and stores the initial request
as its input. Modules may request HTTP work, which the host creates as child tasks;
native actors may create reasoning children with the local planning tool.
Goal evaluation remains separate from procedural completion. Pending tasks have creation order and
no start time; activation adds them to the active lineage and records start time.
The global 20-cycle limit is unchanged. Structural correction retains its separate
three-attempt limit. Ordinary provider failures still abort; module processing
failures alone do not abort the agent.

## Events and acceptance

The host-created envelope is:

```json
{
  "id": "run-id/event/1",
  "sequence": 1,
  "task_id": "run-id/task/1",
  "timestamp": "2026-09-30T00:00:00Z",
  "source": {"kind": "llm", "id": "run-id/operation/1"},
  "payload": {
    "operation": "goal_evaluation",
    "status": "accepted",
    "result": {"achieved": false, "rationale": "More work is needed."}
  }
}
```

Current source kinds are `user`, `llm`, `runtime`, and `http`. Source identity is supplied
by host adapters, never extracted from model JSON. The CLI admits initial user
input, accepted ordinary generation, and completed goal evaluation. Contract
selection/reduction also admit their completed results when supplied a
`stateflow.Client`.

Structured events are emitted **after** validation correction and downstream
checks finish. Raw client responses and rejected correction candidates are not
domain observations. A terminal operation error emits only an operation label,
error status, and host-authored `operation_failed` code, never candidate data or
provider diagnostics. Cancellation is journaled without starting further module
calls. Inference accounting still counts individual attempts; intermediate
candidates are not copied into the journal.

Events broadcast sequentially in definition order. All modules finish processing
the current event before any resulting host work executes. Commits/failures do not
themselves generate events; completed HTTP operations produce correlated events.
The dispatcher iterates over tasks, with no recursive callback or subscription API.
Only the owning partition is writable. Processing failures do not roll back
successful independent partitions.

`stateflow.Client` injects a projection of current actionable state
before every inference, including corrections and all three existing client
methods. The projection includes intrinsic counters, running/pending/waiting task summaries,
and current partition values with freshness metadata. Active task plans and compact
completed reasoning children of the active lineage are included. It excludes completed execution tasks,
task inputs/results, invocation payloads, and the journal. Full snapshots and the
journal remain available for auditing and tracing. Original operation inputs are
retained. Prior-cycle prose and evaluation guidance are replaced each cycle and
bounded to 4096 Unicode characters each; they are not durable knowledge. Modules
must retain durable facts and fixed decision baselines in their current partition
values rather than depend on replay of previous model responses. Modules may emit
bounded `progress` records alongside accepted processing or mutations to declare
established conclusions, completed requirements, and unresolved focus. The model
receives task-relevant `established_progress` and `active_focus` separately from
current knowledge. Records replace prior progress rather than append per cycle;
freshness is reported separately from the module-declared procedural status.
A stale observation or changed knowledge version alone does not semantically
invalidate a fixed historical outcome. See the [module ABI](state-module-abi.md) for fields and limits.
The host never derives these outcomes from tool names or task payloads. Partition values
remain opaque to the host and should represent current world state, not an event
archive. The CLI
runs inference with the active reasoning lineage; HTTP children execute host work without inference or
continuation histories, so they do not consume agent cycles.

## Model-authored plans

Native actors receive `grodt_manage_plan`, a local tool that executes no MCP
operation and consumes no MCP invocation budget. Its actions are `revise`,
`create_child`, `complete_child`, and `abandon_child`. Each call must be alone.
Plans are optional: the runtime neither invents steps nor chooses a strategy.

A task's `plan` records a description, optional natural-language completion
criteria, ordered steps, current step, concise outcome, evidence references and
information gaps. Plan and step statuses are `pending`, `active`, `partial`,
`satisfied`, `invalidated`, or `failed`; execution-task status stays separate.
Descriptions, criteria, outcomes and individual gaps are limited to 512 bytes.
Plans have at most 32 steps; each plan/step has at most eight evidence references
and eight gaps. There are at most 64 model-planned tasks, eight active lineage
levels and 32 local plan calls per generation. No updates are required per cycle.

`revise` supplies the entire current plan with its existing `revision` (zero for
an initial plan). The host checks the revision and increments it on acceptance.
Pending or partial steps may be added, removed, replaced or reordered. Satisfied
and invalidated steps cannot be removed or have their established content
rewritten. Explicit invalidation preserves their criteria, outcome and evidence;
reassessment uses a new step. A satisfied plan follows the same preservation
rule; a new child can represent subsequent intent. Every accepted revision is
journaled, so superseded plan bodies never need to appear in normal requests.

Evidence has the form `{"partition":"world","version":1,"path":"/setting"}`.
The path is a JSON pointer into module-owned state; an empty path selects the
whole value. New references must resolve at the named current version of a
non-stale partition. A tool invocation, unaccepted result or invented pointer
cannot establish evidence. Referenced values retain JSON numeric precision.
Version-zero references may point into validated initial partition state.

New satisfaction claims require an outcome, evidence and no remaining gaps.
The separate LLM evaluator receives the claims, criteria and resolved accepted
evidence, and returns a bounded boolean decision and concise rationale. It
judges whether the evidence supports the intent, without prescribing strategy.
All new claims in a proposal are evaluated together. Rejection retains those
claims as partial with an information gap; malformed proposals leave the plan
unchanged. Evaluator requests/responses use the existing trace and usage path;
accepted decisions and their evidence are journaled. An evaluator unavailable
at the planning boundary cannot approve satisfaction. Planning claims never
mutate knowledge or substitute for the root objective evaluator.

Projected plans retain satisfied outcomes across cycles, even when newer
observations supersede their original evidence. `evidence_state` reports
`current`, `superseded`, `stale`, or `missing` against current partition metadata.
Superseding an observation does not mechanically invalidate a historical
conclusion: the actor decides whether its intent requires reassessment. This
permits both fixed reference observations and later retrievals of changing facts.
There is no duplicate-call suppression. Monitoring and waiting are legitimate
unresolved intents and do not require immediate external execution.

A completed child resumes its parent and retains its compact plan in the parent's
projection. Abandoning a child records incomplete execution rather than semantic
satisfaction. Only the independent root evaluator may end the objective; if it
confirms success while children remain unresolved, those children end incomplete.
All full inputs, execution payloads, evaluations and prior revisions remain
available in the snapshot/journal or trace rather than recursive model context.

## Automatic procedural reconciliation

Native-tool runs reconcile after each completed external invocation batch, after
module acceptance and before the next actor inference. The checkpoint interprets
accumulated current knowledge together with procedural context, actor intent and
recent activity: what is settled, and what remains unresolved. It selects no future
strategy, instruments, analyses, tool calls or execution actions. The existing
configured reconciler and evaluator clients are reused; there is no additional
model, provider, dependency graph or configuration.

The structured `progress_reconciliation` request uses the observed client directly,
without full-state injection. It contains the root request, active objective and
bounded lineage, current plan, task-relevant module progress, latest public actor
intent and invocation names. `accepted_evidence` is one canonical table of exact
current accepted subtrees, including unchanged observations from earlier batches.
`recent` distinguishes changed values and intentional MCP source refreshes. No
conversation, catalog, invocation history, journal, or prior reconciliation request
is included. The actor's ordinary full-knowledge projection is unchanged.

`state_coverage` inventories state partitions and MCP sources with observation
freshness, value type, item count, selected/omitted value counts and complete/partial
coverage. `omitted_sources` reports inventory cardinality overflow. Payload bytes
are allocated equally across partitions and then their MCP sources; selection
proceeds in rounds across sources. A large result cannot consume another source's
byte allocation or all evidence slots solely because it is large. When source
cardinality exceeds the inventory/evidence budget, omissions remain explicit.
Partial coverage never establishes exhaustive inspection or absence of omitted
facts. Oversized scalar text is omitted rather than represented as complete text.

Input data and candidate outputs are limited to 64 KiB each. The original request
is initially clipped to 8 KiB, actor intent to 4 KiB, and plan content to 24 KiB;
encoded-size checks further trim context to reserve accumulated-state space. At
most 64 evidence values of at most 4 KiB are selected. Current-step evidence is
preferred within its source share. JSON numbers retain precision. Overlapping
parent/child values are not duplicated. MCP text is omitted only when it exactly
encodes the accompanying structured content; distinct text remains eligible.

Reconciliation emits at most eight step deltas, a current-step identifier and an
`unresolved_focus` update (`null` preserves, an empty string clears). Plans persist
optional `unresolved_focus` as a bounded descriptive note rather than an artificial
future workflow step. Existing plan descriptions, step intent, criteria and order
remain authoritative. Retrospective outcomes may be recorded without an exact
latest-turn quote when supported by objective, procedural context and accepted
state. Reconciliation cannot replace a strategy or generate competing workflows.
The actor projection indexes `established_steps` and `unresolved_steps` within each
task, referencing its existing plan without duplicating descriptions.

Every reconciliation result, including `no_progress` and identical proposals, passes independent
`procedural_continuity_evaluation` through the existing evaluator. One request
checks both procedural scope (`continuity_valid`) and new satisfaction claims
(`satisfied`); evidence values appear once in its context. Focus-only changes are
also checked. The entire resulting representation must agree with established
outcomes and accepted evidence; unchanged focus and gaps are checked too. A successful
empty collection observes zero matches within its query scope; failed or missing
results are unknown, and partial coverage cannot establish exhaustive absence.
Unsupported strategy or continuity receives bounded correction
feedback before any procedural commit. Unsupported satisfaction retains partial
status and a concise gap, followed by consistency evaluation of the downgraded
representation before commit. Ordinary actor-authored planning retains its existing
completion schema and authority. Module-owned knowledge is never written by these
procedural operations.

Evidence freshness is separate from semantic status. Current/superseded/stale/missing
labels describe observations; changing a partition version does not invalidate a
fixed historical conclusion. Original completed-step provenance is retained,
including version-only replays. Module-declared progress status is preserved in
projection and accompanied by separate freshness. New satisfaction claims still
require current resolvable evidence, outcomes and no remaining gaps. Explicit
semantic invalidation and deliberate repeated retrieval remain available.

`no_progress` requires empty updates, empty current step and null unresolved focus.
It asserts that retained procedural state remains consistent unchanged. Contradictions
receive bounded evaluator feedback through the existing three-attempt correction
path, permitting focus-only correction. Exhaustion fails reconciliation.
Identical normalized plans are not revised, including after an evaluator downgrades
a repeated unsupported satisfaction claim. Duplicate identical outcomes under new
IDs are rejected; the scope evaluator also checks semantic duplicates. Existing
revision, task identity, evidence and completed-history checks remain in force.
Neither reconciliation nor evaluation admits world-state events or recursively
triggers another checkpoint. Provider failures, malformed output and exhausted
corrections retain existing failure behavior; no progress is fabricated as fallback.

The existing trace/accounting path captures request/response bodies and usage.
`procedural_reconciliation` journal entries retain the bounded considered context,
proposed changes and failure code; evaluation and plan revision use their existing
journal entries. These audit bodies are not projected into later actor context.
A meaningful inferred update may require an evaluator request where none was
previously required; satisfaction and scope checks are combined, and normalized
no-ops require no evaluation. The effect on overall cost depends on avoided retries
and repeated setup and is not guaranteed.

Reconciliation performs no MCP calls. Deterministic derivations belong in an
explicitly authorized state/module computation contract. If no accepted computation
establishes a required derived value, reconciliation retains an information gap
rather than deriving domain formulas with LLM arithmetic.

## Processing outcomes

| Result | Value | Metadata |
| --- | --- | --- |
| `ignored` | Unchanged | No successful sequence or staleness changes |
| `processed` | Unchanged | Advance processing sequence; clear staleness/failures |
| committed `mutation` | Validated candidate | Advance processing/update sequences and version; clear staleness/failures |
| `error` | Retain last valid value | Set stale; increment failures; retain successful sequences |

**Processed is an explicit module assertion that the event was successfully
incorporated or evaluated and that the partition is again synchronized enough
to clear GRODT staleness. Merely recognizing an event is not sufficient; a module
should return Ignored if the event does not establish that condition.** A committed
mutation carries the same synchronization assertion. There is no `recovers` flag.

Metadata includes `version`, `last_processed_sequence`, `last_update_sequence`,
`last_update_at`, `stale`, `consecutive_failures`, and a single `last_error`.
Both successful sequence fields refer to source event ordering. Initialization is
version zero with successful sequences zero. Domain freshness is represented by
the domain value, independently of GRODT staleness.

Module-reported error, WASM runtime failure, and rejected mutation have distinct
failure kinds. Errors exposed in state are host-selected codes, not guest text,
stack traces, or transport envelopes. A timeout during one module call marks that
partition stale and processing continues for others. Parent-context cancellation
stops remaining delivery; already committed partitions are retained.

## Mutation and journal

Modules return complete replacement or RFC 6902 JSON Patch, never both. Patch
application uses `evanphx/json-patch/v5`, with negative indices, missing-path
removal, and automatic ancestor creation disabled. A patch applies to a private
candidate; the complete result must validate before the partition is committed.
No valid portion of a failed patch is committed. Large JSON integers retain their
original precision.

Paths are rooted at the domain value. A `/metadata` field in a domain value is
merely domain JSON; it cannot access the host's metadata. Modules cannot create
or remove configured partitions. `add` has RFC 6902 semantics (including replacing
an existing object member), not a bespoke create-only operation. Existing contract
`PartialSpecification` still defines optional proposal fields, not patch or merge
semantics. Accepted LLM proposals are observations, never automatic state commits.

Event sequence orders observations. A separate monotonically increasing journal
sequence orders initialization, event receipt, task lifecycle/cycles, processing
outcomes, mutations, and failure metadata. Mutations retain their accepted result
and associated metadata in the journal. Snapshot changes and their entry are
published synchronously without interleaving. Historical failures belong in this
append-only in-memory journal, not in current partition metadata. The journal is
not sent wholesale to the LLM and is not used as a replay-based database.

All store/client operations require sequential use. There is no concurrency,
locking protocol, persistence interface, durability guarantee, or hidden state in
WASM instances. Future persistence must durably pair snapshot transitions with
journal entries; this implementation makes no crash-atomicity claim.

See [the module ABI](state-module-abi.md) for sandbox limits and the reference
fixture. Dynamic module authoring by a SKILL is future work, not part of this
checkpoint.

## Module-requested HTTP work

Enable the capability explicitly with `--allow-state-http` alongside
`--state-definition`, or `state.Options{AllowHTTP: true}` for an embedded host.
WASM retains its import-free ABI and has no direct I/O. The default rejects HTTP
execution with a correlated `http_disabled` result. No LLM credentials or cookies
are injected, and the HTTP transport does not use environment proxies.

Successful `ignored`, `processed`, and `mutation` results can include up to eight
`requests`. Request-only `ignored` makes no synchronization assertion and cannot
clear staleness. A malformed result, conflicting request identity, or rejected
mutation queues no work from that result. A committed mutation survives later
HTTP denial or failure. `error` results cannot request work.

Each accepted request becomes a pending task with its request, requesting
partition, and originating event in `work`. `parent_id` is the causal event's
`task_id`, captured before broadcast. Requests from the same event are siblings,
even when different modules submit them. There is no second queue. Children run
sequentially in creation order; the parent waits. A child stores its HTTP outcome
in `result`, then broadcasts it while still active. Requests produced by that
result become its children and finish before it closes and resumes its parent.
This naturally supports pagination and other multi-step acquisition.

Request IDs match `[A-Za-z0-9_-]{1,64}` and are scoped by partition for the whole
run. Repeating an identical ID/kind/payload reuses the task without another HTTP
execution or result broadcast. Object key order and insignificant whitespace do
not affect payload comparison; JSON number spellings are retained. Conflicting
reuse is an invalid module result. A new logical operation needs a new ID.
Stored outcomes prevent re-execution when the task resumes.

HTTP results use `source.kind: "http"`, `source.id` and `task_id` equal to the host
task ID, and `correlation: {partition, request_id}`. Every module receives the
result through ordinary broadcast. Payload contains `status_code`, bounded
`headers`, base64 `body` bytes, and/or a sanitized `error` code. Empty optional
fields are omitted. Non-2xx responses remain observable HTTP responses; status
codes 400 and above or host errors mark the child failed, without failing its
parent. No HTTP failure directly changes partition values or staleness; modules
choose how to consume the result under the existing validation rules.

| Host resource | Limit |
| --- | --- |
| Requests per module result | `MaxRequestsPerResult = 8` |
| Logical host executions per run | `MaxHostExecutions = 32` |
| Request duration, including redirects and body | `HTTPTimeout = 10s`, or earlier caller deadline |
| Redirects per logical request | `MaxHTTPRedirects = 3` |
| Response body | `MaxHTTPBodyBytes = 1 MiB` |
| Request/response headers | `MaxHTTPHeaderBytes = 64 KiB` |

The host budget is independent of the 20 agent cycles and three structured
attempts: host requests require no inference, but result-driven work chains must
still terminate. Denied/malformed HTTP attempts also consume one host execution,
preventing unbounded failure-driven chains. Redirect hops consume no additional
executions. Exhaustion is the distinct terminal `ErrHostBudget` runtime failure;
remaining tasks fail and prior commits remain. Cancellation stops delivery and
further work; an HTTP outcome already obtained remains recorded on its task.

Initial and redirected URLs pass the same capability/URL checks (HTTP(S), a host,
and no embedded user credentials). Future destination policy must extend this
shared check. Redirect method/body behavior follows Go's HTTP client. No automatic
application retries, allowlists, secret injection framework, persistence, MCP,
or concurrent execution are added in this checkpoint.

Deterministic tests use local HTTP servers and the import-free WASM reference:

```sh
go test ./internal/state/... ./internal/stateflow/... ./cmd/grodt/...
```

## Stateful feedback milestone

The gathering integration scenario exercises the production cycle runner,
`loop.StructuredProvider`, `stateflow.Client`, manifest-loaded WASM, event journal,
and validated atomic commits. Both deterministic and live modes share this path.
The ordinary CLI still uses its existing generic provider and LLM goal evaluator.
An injected completion evaluator allows this scenario to check observable outcomes
independently; it does not replace ordinary CLI behavior.

The objective is to obtain three units of wood and return to camp. Initial
knowledge contains only camp, zero wood, and no known locations. The action
contract is `{action, target}` with explore/move/gather/finish actions and a free
string target; neither the prompt nor the action schema reveals the forest,
direction, or resource-location mapping. The deterministic environment knows the
world, interprets accepted actions, and emits runtime events. It cannot access or
edit the Store. The real WASM fixture alone proposes partition mutations from
those environment events. Accepted LLM actions and unrelated events are ignored
by the fixture.

`finish` requests evaluation. Completion requires authoritative knowledge and
independent environment truth to agree that the agent is at camp with at least
three wood. An early finish returns an incomplete evaluation and continues;
repeated early finishes eventually reach the unchanged 20-cycle limit. Structural
corrections remain limited to three attempts per operation; invalid candidates
never execute. Invalid world actions generate deterministic rejection events.

Rejected completion requests now identify insufficient recorded wood, an incorrect
recorded location, or both. Stale partition values and disagreement with independent
environment verification are reported separately. Explanations state current values
and objective requirements without prescribing an action or revealing unobserved
world values. The successful rationale and completion predicates are unchanged.
The runner's existing continuation history carries the exact rejection rationale
into the next structured inference context. Deterministic tests verify that delivery
and that requesting/rejecting completion changes neither partition values nor
partition metadata.

The scripted decision source reads the same composed snapshot delivered to the
live client. It chooses exploration without discovered knowledge and movement
when discovery provides a resource location. Assertions connect discovery event,
validated commit, subsequent inference input, movement, gathering, return, and
verified finish. Journal checks ensure every domain mutation follows an
environment-authored event. No exact natural-language rationale or fixed live
action sequence is required.

Run deterministic tests without external LLM access:

```sh
go test ./cmd/grodt -run 'Test(StatefulFeedbackLoop|PrematureFinish|RepeatedPrematureFinish|Gathering|InvalidGathering|FeedbackCorrection)' -count=1 -v
go test -race ./...
```

Run the live acceptance milestone explicitly:

```sh
go test -tags live ./cmd/grodt -run '^TestLiveStatefulFeedbackLoop$' -count=1 -v
```

It uses the existing YAML/environment resolution (`config.yaml` at the repository
root by default, or `GRODT_LIVE_CONFIG`), with a two-minute overall deadline. Logs
show each returned structured LLM response (including correction candidates),
cycles, actions, environment event IDs, task IDs, partition versions, location,
wood count, discovered locations, and the final goal status. No reasoning output
is requested. Missing configuration skips the live test and does **not** establish
milestone success. Ordinary tests never invoke the live client. A failed live run
is reported as evidence; there is no autonomous coaching/retry loop.

The module source, checked-in binary, and rebuild command are in
[`internal/state/wasm/testdata/gathering`](../internal/state/wasm/testdata/gathering/README.md).
The scenario does not exercise HTTP host work or add MCP, persistence, concurrency,
or a simulation framework.

### Observed actionable-feedback rerun

The unchanged live command passed after the factual completion-feedback change,
using eight cycles and eight LLM requests. Discovery entered the partition in cycle
1, and the next decision moved to the discovered location. By cycle 5, the recorded
inventory held three wood. At cycle 6, the model requested completion while still
in the forest. The evaluator rejected it with:

> Completion rejected. Recorded inventory contains 3 units of wood, satisfying the requirement of at least 3. The recorded location is "forest"; the objective requires the current location to be "camp".

The next decision selected a move to camp. A further completion request succeeded
at cycle 8, with authoritative state and environment truth both showing camp and
three wood. Partition version was 6. This records one observed live run; acceptance
continues to check causal milestones and independently verified completion without
requiring these cycle counts or this action sequence.

The deterministic suite, `go test -race ./...`, and `go vet ./...` also passed.
