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
five-attempt limit. Ordinary provider failures still abort; module processing
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
operation and consumes no MCP invocation budget. Its actions are `revise`, `patch`,
`create_child`, `complete_child`, and `abandon_child`. Each call must be alone.
Plans are optional: the runtime neither invents steps nor chooses a strategy.

A task's `plan` records a description, optional natural-language completion
criteria, ordered steps, current step, concise outcome, evidence references and
information gaps. Plan and step statuses are `pending`, `active`, `partial`,
`satisfied`, `invalidated`, or `failed`; execution-task status stays separate.
Descriptions, criteria, outcomes and individual gaps are limited to 512 bytes.
Plans have at most 32 steps; each plan/step has at most eight evidence references
and eight gaps. There are at most 64 model-planned tasks and eight active lineage
levels. Planning permits 32 rejections since the last accepted planning operation;
acceptance resets that rejection count. External tool success does not reset it.
Accepted planning operations do not exhaust this allowance. No updates are required
per cycle.

`revise` supplies the entire current plan with its existing `revision` (zero for
an initial plan). The host checks the revision and increments it on acceptance.
Pending or partial steps may be added. Removing, rewriting, abandoning, or reordering
existing intent requires an actor-authored `revision_reason` on `revise` or `patch` and scope
evaluation; selecting another current step does not withdraw other steps. Satisfied
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
also checked. Explicit actor-authored higher-level purpose from the existing plan
or current actor intent is preserved across tool boundaries in `unresolved_focus`.
Successful acquisition resolves acquisition, not its stated purpose, unless accepted
evidence supports resolution. Focus may be established without an existing plan;
`no_progress` cannot omit or erase unresolved purpose. Retained purpose survives
successive acquisitions, including turns with little or no actor prose. Concise
paraphrases are allowed, but tool names and the broad objective alone cannot
establish a purpose. This does not prescribe next actions or require comprehensive
procedural coverage.

The entire resulting representation must agree with established
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
receive bounded evaluator feedback through the ten-attempt correction
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
corrections permit five attempts per operation; invalid candidates
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

### Invocation failure recovery

Recoverable MCP invocation failures return synthetic tool feedback containing a
host-defined category, dispatch certainty (`not_dispatched`, `outcome_unknown`,
or `result_rejected`), and `result_available: false`. Safe diagnostics include
exact accepted-envelope byte counts or observed wire-byte lower bounds with their
limits, and allowlisted validation keyword names. Raw remote exceptions, rejected
payloads, schema instance paths and arbitrary diagnostic values are not exposed.
The failure is retained in invocation tasks and the journal, never admitted as
world knowledge. Reconciliation receives bounded current-batch `action_failures`
as procedural context so historical observations cannot imply failed acquisition
succeeded. Successful earlier batch members remain admitted; later siblings are
recorded as not dispatched and are not invoked.

Recovery permits two delivered actor responses to failure feedback, rather than
two failed calls or batch members. Failed model-generation attempts do not consume
these opportunities. Planning or catalog turns do consume delivered opportunities;
the latest failure summary remains available across those boundaries. A valid MCP
response, including `isError: true`, resets recovery. No external call is replayed
automatically; the actor must reassess arguments and verify authoritative state
before considering a repeated external effect whose outcome is uncertain.
Cancellation, runtime budget exhaustion, sensitive-result rejection, and recovery
exhaustion remain terminal. This adds no procedural stall detection or settings.

### Rejected exclusive host operations

A mixed batch containing one `grodt_manage_plan` call is rejected before any
operation executes. The runtime retains the bounded attempted planning arguments
as temporary correction context, not accepted procedural state. Feedback reports
`status: failed`, `execution: not_dispatched`, and
`reason: exclusive_host_operation`, together with detectable planning-content
errors such as newly satisfied steps without outcomes or evidence.

Before ordinary generation resumes, correction requests advertise only
`grodt_manage_plan`; the host also rejects bypass attempts before catalog routing
or external dispatch. The actor must author a standalone correction, which passes
the existing planning and completion checks. Acceptance clears pending correction;
companion calls execute only if the actor explicitly requests them afterward.
No domain admission event is emitted for rejected batches.

Ten usable actor correction responses are permitted for exclusive host correction.
Failed generation requests, empty responses, and printed tool markup do not consume
those opportunities;
existing generation-correction limits separately bound unusable responses.
Wrong tools, mixed batches, invalid planning content, and ordinary final text
are usable responses that fail correction. Exhaustion terminates without
resuming unrelated work. Existing external-invocation recovery remains separate
and is suspended while exclusive host correction is pending.

Oversized or malformed arguments are not retained as truncated JSON; safe byte
counts and feedback require actor reconstruction. Multiple planning operations
in one batch are terminally rejected as ambiguous. Host-selected correction
lifecycle codes are retained through existing runtime journal diagnostics; no
pending proposal is admitted as accepted state.

### Actor intent capture and plan continuity

Before dispatching external calls accompanied by actor prose, a bounded semantic
assessment checks whether explicitly declared steps or unresolved purpose are
represented by the existing plan/focus. It does not infer intent from the broad
objective or tool names, prescribe strategy, or require a plan for a single immediate
acquisition without higher-level purpose. Missing representation defers the entire
batch and enters the existing exclusive host correction path. The actor must author
a standalone `revise` or `patch`; its resulting plan is assessed against the retained prose
before acceptance. Deferred calls execute only if the actor explicitly requests
them afterward.

Rejected standalone planning operations also enter that correction path. One full
proposal is retained as temporary correction data, separately from accepted state,
and corrections must preserve unaffected step IDs, intent, criteria and order.
Readable intent is retained even when other schema fields or evidence references
are invalid. Explicit scope changes require `revision_reason` and independent
evaluation against accepted intent and, when applicable, the rejected proposal.
Outcome/evidence repairs do not require erroneous satisfaction claims to survive.
Existing structural protections for satisfied/invalidated history still apply.

These corrections permit ten usable actor responses, including for standalone
rejections; failed generation requests and empty/printed-markup responses retain
their existing separate generation-correction limits. Standalone correction now
bounds repeated invalid proposals before the general 32-rejection allowance can
be exhausted. Acceptance clears correction context and resets the rejection count.
Correction completion cannot be bypassed with unrelated tools or final prose.

Actor prose is retained exactly up to 4 KiB, and proposed arguments exactly up to
the existing 64 KiB argument limit. Oversized material requires actor reconstruction;
truncated proposals are never treated as full intent. Correction history carries
bounded metadata instead of another copy of the proposal. Capture/revision model
inputs have a 128 KiB limit and strip outcome/evidence payloads from intent views.
Scope review uses the existing fair, bounded canonical evidence projection with
explicit coverage and omission metadata; partial evidence cannot prove exhaustive
absence. It adds semantic assessments for prose-bearing acquisition boundaries,
capture corrections, and explained scope changes, using the existing evaluator
client. No second model or dependency framework is introduced.

Reconciliation evaluation requires `purpose_preserved` separately from
`continuity_valid` and `satisfied`, including for `no_progress`. An empty set of
new satisfaction claims cannot excuse missing actor-authored unresolved purpose.
False purpose preservation uses the existing bounded reconciliation correction
path and fails if correction is exhausted. Successful empty collections remain
scoped zero-match observations; failed/missing results remain unknown. Root objective
completion remains independently evaluated through its existing evaluator.

The optional `revision_reason` is a host-command field, not a persisted Plan field.
Existing stored plans remain readable without migration. Actor commands that
previously silently removed pending details now require correction/explanation;
internal continuity evaluator responses must include the new purpose judgment.
Plan capture and scope judgments remain semantic model decisions, audited in the
existing journal and traces rather than assumed reliable from schema validation.

### Accepted procedure requirement

The tool reasoning loop requires an accepted plan with at least one pending,
active, or partial step before ordinary actor execution. This is checked at
every actor boundary, including after reconciliation and child-task transitions.
When the plan is absent or exhausted, the existing root objective evaluator runs
first. Confirmed root completion finishes normally without a second evaluation.
An incomplete objective exposes only `grodt_manage_plan` until the actor submits
an accepted current procedure with unfinished work; external calls and final
actor responses cannot bypass renewal. Evaluator errors remain terminal.

Procedures may cover short phases. Actor completion-only revisions may record
the last evidence-backed result of an existing procedure; they trigger the same
root check before more ordinary execution. A satisfied or invalidated plan may
reopen with new unfinished steps, preserving completed step history and prior
plan revisions in the journal. New completion claims still require evidence
evaluation. Closing an already completed child or abandoning a child remains
available through the planning tool; the resumed parent is checked again.

Each initialization/renewal episode permits ten usable actor responses under the
existing host correction limit. Acceptance resets that episode. Deferred calls
are never replayed. Existing empty/exhausted plans remain readable but require
renewal if the root goal is incomplete. No persisted schema or migration changes
are needed. Root evaluation adds one model check per missing/exhausted-plan
boundary, using the existing state projection and evaluation trace mechanisms.

### Terminal milestones

The CLI reports accepted plan revisions, step status changes, current-step and
unresolved-focus changes on stderr using the existing `[grodt]` prefix. Task IDs
distinguish separate plans. Host planning processing reports `Planning started`
before validation/evaluation and `Planning completed` after acceptance or rejection,
with a bounded, redacted rejection reason. Start events flush synchronously before
processing, and completion events flush before another actor request. Mixed-batch
rejections handled before planning processing retain their correction milestones.
Unchanged revisions and repeated observations do not
produce progress milestones; rejected proposals never produce satisfied-step
messages. Planning corrections, reconciliation corrections, and tool recovery
report host-selected diagnostics, including available byte limits and validation
keywords. Attempt numbers describe correction responses rather than batch failure
counts.

Reporting reads selected journal entries incrementally. Plan entries project only
the accepted plan, excluding archived task inputs and results. Tool recovery uses
a bounded host-authored diagnostic summary. Text is redacted before truncation,
normalized to one line, and bounded. Pending milestones are flushed before the
next model request and before the final run report; diagnostics writer failures
propagate through the existing CLI error path. Reporting changes neither model
context nor completion decisions and adds no inference requests.

### Authoritative ordered plan continuity

The task store owns the accepted ordered plan. Ordinary prose, unmentioned steps,
and omitted reconciliation updates do not change it. Every ordinary actor turn
receives the retained plan, its revision, current step and position, and the
established/unresolved step indexes. Step bodies appear once; evidence remains
referenced rather than copied into an additional procedural summary.

An accepted plan always contains at least one step. Ordinary execution requires
an unresolved step (pending, active or partial) and an actionable current
position. The runtime preserves a valid current selection and otherwise advances
to the first unresolved step in accepted order. Completing or terminating a
step does not erase it. When no actionable work remains, the existing root
objective evaluator determines completion or requests plan renewal.

Call grodt_manage_plan alone. Use revise for initialization or a complete
snapshot containing every accepted step. Missing accepted IDs are rejected:
revision_reason does not authorize implicit deletion. Use action patch for
bounded changes without repeating unrelated state. A patch supplies the accepted
revision and may contain add_steps, step_updates, remove_steps, unresolved_order,
and plan_updates. At most eight step mutations are allowed. Step updates identify
an accepted ID; absent fields retain their values. Explicit empty strings or
arrays clear eligible fields. unresolved_order must list every unresolved ID
exactly once and changes only unresolved slots; terminal history remains in
place. plan_updates can update metadata, focus or select an unresolved
current_step. A patch cannot remove terminal history or the final retained step.

Patches are applied to an independent candidate and committed atomically only
after structural validation and the existing intent/completion evaluations.
Material strategy changes, withdrawals and reordering require revision_reason.
New satisfaction claims still require an outcome, accepted evidence, no gaps and
independent evaluation. Explicit invalidation retains original provenance.
Rejected updates leave accepted state and its revision unchanged. Previous plan
versions and evaluated transition reasons remain in the existing journal/trace.

Tactical choices within the current step, such as tools, indicators, timeframes
or sources, need no procedure revision. Explicit durable changes outside the
accepted procedure require a standalone planning update before external dispatch.
Free-form conflict recognition uses the existing semantic evaluator; state
preservation and ordering do not depend on that evaluator reconstructing plans.

Reconciliation updates only accepted step IDs, outcomes, gaps, status and focus.
It cannot create, remove or reorder procedural steps. no_progress remains subject
to consistency evaluation. Evidence freshness remains distinct from semantic
invalidation; bounded evidence projections retain fair source coverage and
explicit omission metadata.

Child-task creation creates an unplanned reasoning task; its next actor turn
must establish a nonempty accepted plan before ordinary execution. Existing
persisted Plan fields remain compatible, while previously accepted empty plans
enter initialization mode. Missing current positions are derived from order.
The actor tool schema adds patch and enforces minItems: 1 on snapshot steps.
The existing 32-step limit includes terminal history; history is never silently
discarded to fit this bound.

### Actionable reconciliation corrections

Every host validation rejection returned through reconciliation's existing
correction path identifies the defect and a required repair. Protected
description/completion_criteria mismatches report the step ID, field, exact
accepted value (including an empty string for absent criteria), and a bounded
received value. Multiple detected field mismatches are reported together rather
than spending a separate correction turn on each field.

Corrections tell the reconciler to preserve supported status, outcome, evidence
and gap updates while restoring protected intent exactly. Where it fits, the
rejected proposal is included as correction data, never accepted state or
instruction authority. Oversized proposals are explicitly omitted with their
byte count; additional omitted diagnostics and truncated received values are
also labelled. Expected values in reported field diagnostics remain exact.
Host correction feedback is bounded to 8 KiB and replaced each attempt rather
than accumulated. It does not duplicate tool payloads or expose steps omitted
from the bounded reconciliation projection.

Reference failures identify the affected step and supplied reference and explain
the accepted reference/version/path boundary. Other constraints explain repairs
for action/update mismatches, update limits, completion evidence, historical
immutability and current-position selection. Schema-invalid structured output
continues through the existing schema-validation feedback path; semantic
continuity rejection continues to return the evaluator's bounded rationale.

No rejected proposal is partially committed. The reconciler must resubmit a
valid candidate, with independent evaluation of progress and completion still
required. Procedural reconciliation permits ten total attempts per operation (the initial
proposal plus nine corrections). The independent structured schema-validation
limit is five attempts (one initial inference plus four corrections). Evidence semantics, acceptance rules and failure
behavior remain unchanged; no stall detection is added.
