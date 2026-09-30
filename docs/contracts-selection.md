# Contract Selection and Semantic Reduction

## Overview

Grodt uses an LLM to perform semantic contract selection and structured reduction over arbitrary information.

The goal is to keep semantic interpretation in the model while keeping legal structure and execution boundaries deterministic.

The core flow is:

```text
information
    ↓
semantic contract selection
    ↓
writability filtering
    ↓
structured reduction
    ↓
structural validation (bounded correction if needed)
    ↓
proposed mutation
```

Component 004 (`internal/contracts`) is responsible only for:

- selecting relevant declared contracts;
- mechanically composing a response specification from selected writable contracts;
- asking the LLM to reduce arbitrary information into that specification;
- structurally validating generated selection and proposal JSON;
- returning each accepted proposal with its original bytes.

It does not:

- validate domain meaning or mutation authority;
- apply mutations;
- define merge or replacement semantics;
- persist state;
- calculate derived values;
- enforce domain invariants.

Those responsibilities belong to later components.

---

## Design Principle

The guiding principle is:

> Hardcode what is legal; let the model determine what is appropriate.

Contract definitions describe the legal state surface.

The LLM determines which parts of that surface may be relevant to new information.

This avoids handwritten semantic routing such as:

```text
if response is an account response:
    update account

if response mentions holdings:
    update positions

if response contains a preference:
    update memory
```

Instead, the model receives compact contract descriptions and determines relevance from meaning.

---

## Contract Definition

A contract currently has the form:

```go
type Contract struct {
    Name          string
    Description   string
    Schema        json.RawMessage
    ModelWritable bool
}
```

The schema defines the shape of the contract.

`ModelWritable` determines whether the contract may appear in an LLM-generated mutation proposal.

A contract may still be semantically relevant even if it is not model-writable.

For example:

```text
objective
    relevant to progress
    not writable by the model

account
    relevant to account observations
    writable

positions
    relevant to holding observations
    writable
```

This separation is intentional.

Semantic relevance and mutation authority are different concerns.

---

## Selection

Selection is a high-recall semantic operation.

The current instruction is intentionally small:

```text
Select zero, one, or multiple declared contracts that may be affected by the new
information and semantic context. Favor recall when there is plausible durable
relevance. Selection does not imply that a contract must change. Include
read-only contracts when relevant, but do not invent names. Return an empty
relevant array for information with no durable relevance.
```

The model receives only compact descriptors:

```json
{
  "name": "...",
  "description": "...",
  "model_writable": true
}
```

It does not receive the full contract schemas during selection.

The output schema constrains the model to zero, one, or multiple declared contract names.

Conceptually:

```json
{
  "relevant": [
    "account",
    "positions"
  ]
}
```

The result is normalized into declaration order.

---

## Why Selection Is High Recall

Selection is intentionally not a perfect classifier.

Its job is to identify contracts that may matter.

It is acceptable for selection to include a contract that later produces no mutation.

For example:

```text
Account observation
    → objective + account
```

The objective may be semantically relevant because account equity represents progress toward the run objective.

However, because objective is not model-writable, it is removed mechanically before mutation generation.

This gives:

```text
Selection:
    objective
    account

Writable subset:
    account

Mutation proposal:
    {
        "account": ...
    }
```

This is desirable behavior.

---

## Reduction

Reduction operates on an explicit set of selected contract names.

It does not perform selection itself.

The caller may:

- call `Select` and then `Reduce`;
- call `Reduce` directly with known contract names;
- call `PartialSpecification` independently;
- perform zero, one, or several contract operations during a loop.

There is no hidden pipeline state.

For selected contracts, `PartialSpecification`:

1. validates the names mechanically;
2. filters out non-model-writable contracts;
3. embeds the writable contract schemas into a combined response specification.

`Reduce` then asks the LLM to propose only information that belongs in that writable subset.

If no writable contracts remain, reduction returns:

```json
{}
```

without making an LLM request.

---

## Memory

Memory is a special semantic category because it can contain durable information that does not belong in a more specific structured contract.

A synthetic memory contract used during live testing was:

```go
Contract{
    Name: "memory",
    Description: "Durable user preferences and guidance for future reasoning. Excludes balances, holdings, and transient diagnostics.",
    ModelWritable: true,
    Schema: json.RawMessage(`{
        "type":"object",
        "properties":{
            "note":{"type":"string"}
        },
        "required":["note"],
        "additionalProperties":false
    }`),
}
```

The memory contract is intentionally broad.

The model determines whether information is worth preserving.

The runtime does not contain handwritten rules such as:

```text
if user preference -> memory
```

Future memory policy may impose deterministic limits such as maximum serialized size, but semantic curation should remain model-driven. 

**Note:** The synthetic memory contract demonstrated materially higher variance than concrete state contracts.


---

## Live Acceptance

Live tests exercise real semantic behavior against the configured OpenAI-compatible model.

The live scenarios currently include:

### Account Observation

Input:

```json
{
  "equity": 11000,
  "cash": 7000,
  "buying_power": 14000
}
```

Expected semantic behavior:

```text
account must be selected
objective may also be selected
```

Observed example:

```text
Selection: [objective account]
```

Reduction correctly proposed only the writable account contract.

### Position Closure

Input contains:

- confirmed position closure;
- authoritative account values;
- authoritative empty holdings.

Expected semantic behavior:

```text
account must be selected
positions must be selected
```

Observed example:

```text
Selection: [account positions]
```

Reduction proposed both:

```json
{
  "account": {
    "equity": 11000,
    "cash": 11000,
    "buying_power": 22000
  },
  "positions": []
}
```

### Durable Memory

Input:

```text
"For all future reports, use UTC timestamps."
```

Expected behavior:

```text
memory must be selected
```

Observed:

```text
Selection: [memory]
```

Reduction preserved the preference in the memory proposal.

### No Durable Change

Input:

```json
{
  "heartbeat": "ok",
  "request_latency_ms": 12
}
```

Expected behavior:

```text
no contracts selected
```

Observed:

```text
Selection: []
```

Because no contracts were selected, reduction made no LLM call and returned:

```json
{}
```

---

## Live Test Philosophy

Live semantic tests should not require the model to reproduce one exact contract set when multiple interpretations are valid.

For example:

```text
AccountObservation
```

may reasonably produce either:

```text
account
```

or:

```text
objective + account
```

Therefore live tests should generally express:

```text
required contracts
forbidden contracts
```

rather than exact set equality.

The important questions are:

> Did the model miss something that clearly matters?

and:

> Did the model select something clearly incompatible with the declared contract semantics?

Exact mechanics remain covered by deterministic unit tests.

---

## Prompt Tuning Lesson

During live testing, the selection instructions were expanded in an attempt to prevent the model from selecting `memory` alongside account and position contracts.

The expanded prompt included rules such as:

```text
Prefer the most specific declared contract.
Do not use memory to duplicate information represented elsewhere.
Select memory only for preferences, conclusions, lessons, or guidance.
```

This did not improve behavior.

Instead, the model began consistently selecting:

```text
AccountObservation → account + memory
PositionClosure    → account + positions + memory
```

while prompt token usage increased.

Reverting to the original compact selection instruction produced:

```text
AccountObservation → objective + account
PositionClosure    → account + positions
MemoryRelevant     → memory
NoDurableChange    → []
```

This reinforced an important design principle:

> Do not prompt-engineer deterministic routing back into a semantic component.

The contract descriptions and response constraints should provide boundaries.

The model should retain room to perform semantic interpretation.

If increasingly specific prompt rules are required to reproduce deterministic routing, the abstraction is probably being pushed in the wrong direction.

---

## Current Boundary

Component 004 outputs a proposed mutation.

That proposal is not trusted state.

The flow now includes structural validation; state application remains future work:

```text
Contract Definition
        ↓
Selection
        ↓
Reduction
        ↓
Proposed Mutation
        ↓
Validation
        ↓
State Application
```

Validation and state application are intentionally separate concerns.

Component 004 does not decide:

- whether a proposal is authoritative;
- whether values are mathematically correct;
- whether existing fields may be removed;
- whether a contract is merged or replaced;
- whether a proposal violates state invariants.

Those questions belong to subsequent components.

---

## Architectural Takeaway

The contract system is an experiment in separating:

```text
deterministic legality
```

from:

```text
semantic appropriateness
```

Go defines the legal surface.

The LLM interprets arbitrary information and proposes how it maps onto that surface.

Structural validation now gates generated proposals; future domain checks may decide further acceptability.

Future state logic decides how accepted information is applied.

This keeps semantic flexibility in the model without allowing the model to define the system's structural boundaries.
## Structural validation and correction

Goal evaluation, contract selection, and contract reduction validate successful
structured responses against the same specification supplied to the LLM. The
validator remains independent of contracts, providers, and state. Accepted JSON
is returned unchanged; existing selection ordering and evaluation checks still
apply. No authoritative state is applied or changed by this integration.

`internal/structured.MaxStructuredAttempts = 3` allows one initial inference and
at most two corrections per operation. This is independent of the CLI's 20-cycle
limit: correction repeats one structured operation, while an agent cycle performs
generation and evaluation, and contract operations can run independently.

Corrections retain the original operation inputs and specification. Instructions
include a separate JSON envelope containing the exact previous candidate as a
string, native structured validation diagnostics, and a correction instruction.
Only syntactically valid JSON that violates its schema receives this feedback.
Malformed JSON and provider/transport errors remain terminal; there is no JSON
repair or automatic provider retry. Schema errors also abort without correction.
After three invalid candidates, the operation returns an attempt-limit error and
no candidate; the CLI discards the failed cycle without publishing its response.

Every inference is counted. Contract results report `Requests`, the known token
subtotal in `Usage`, and `UsageRequests` for coverage. Nil usage remains
unavailable, not a zero-token estimate. Empty writable subsets still return `{}`
without inference. CLI evaluation corrections use its existing observed client.

The opt-in smoke test uses the normal configured client and configuration resolver:

```sh
go test -tags live ./cmd/grodt -run '^TestLiveStructuralValidation$' -count=1 -v
```

It defaults to the repository's `config.yaml`; `GRODT_LIVE_CONFIG` can select a
separate file. Normal YAML/environment precedence applies. Missing configuration
skips the test. The live test is excluded from ordinary `go test ./...` runs.
`SKILL.state` remains future context only; this checkpoint defines no state behavior.
