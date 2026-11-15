# Open Items — grodt/gr33d (2026-11-14)

Status snapshot after the P0/P1/P2 defect-fix session. Decisions are made item-by-item with the user before action.

**Approved 2026-11-14 07:41 and DONE:** C1 (grodt `0912e74`), C2 (gr33d `f8717582`), C3 (gr33d `e5ab69b5`), D1 (gr33d `0cfd965f`).

## Done (no action needed)

- **is_simulation removal** — removed from `GET /api/market/state` document, module `schema.json`, and tests; kept internal to wharf (`Store.IsSimMode` for Kafka topic routing + `/api/system/lifecycle` for the UI). Wharf suite green. No WASM rebuild needed (module.c is a verbatim passthrough).
- **P2b paper gate / enforcement** — explicitly deferred by user ("enforcement is not required… tackled later").
- **P2 goal accounting (cash vs equity basis)** — deferred.
- P0 fixes (buying_power, ghost position, preflight correction budget, realized PnL) — code complete, uncommitted (see C2).
- P1 fixes (balances 404, planner correlation, strategy_context durability) — code complete, uncommitted (see C1).
- P2a indicator provenance consistency — code complete, uncommitted (see C2).

## Open items

### A1 — Preflight failure (only run-blocker)

- Last run `232cb547…` died at request 119: `tool operation failed during tool call preflight; cycle discarded`.
- Final call was `grodt_tool_018` (`submit_order`, `dry_run: true`, SELL 2.74 ETHUSD @ 1906.5).
- Root cause **not identifiable from artifacts**: `ToolOperationError.Error()` deliberately hides the cause (only the reconciliation-deadline cause is surfaced), and the correction feedback path logs nothing to the run log or journal.
- Next step (when approved): add a diagnostic that records the preflight cause code, or reproduce.

### B1 — config.yaml secret-leak hazard

- `config.yaml` is **git-tracked** despite being in `.gitignore` (tracked before the ignore rule was added).
- Committed version has placeholders; working copy has **live keys** (Strata + `GR33D_MCP_API_KEY`). `git add -A` stages it (confirmed via dry-run).
- Fix: `git rm --cached config.yaml` (does not rewrite history).

### C — Commit / checkpoint decisions

- **C1 — grodt strategy_context commit.** 10 modified + 4 new files (`strategy_context.go` + tests, `state/store.go` RestoreKnowledge, config, README, `.gitignore` `/state/`). Builds, `go vet`, targeted tests green. Committed as grodt `0912e74` (rollback checkpoint).
- **C2 — gr33d P0/P2a commit.** 14 modified files, +1097/−210, HEAD `a94dc30c`. Includes C++ (`market_alpaca.c` fill/symbol fix, `market_main.c` realized_pnl/fees publish, `market_processor.h`) and `ui/tycoon AppShell.tsx`. Committed as gr33d `f8717582`.
- **C3 — gr33d `modules/` untracked.** grodt `config.yaml:17` loads `../gr33d/modules/gr33d-market-state/state.json`; a fresh gr33d clone cannot run grodt. Committed as gr33d `e5ab69b5` (incl. 10.6 KB wasm artifact).
- **C4 — `0001-grodt.code-workspace` + `prompt2.txt` untracked.** Commit or ignore? (not yet decided)

### D — Cleanup

- **D1 — `wharf/cmd/posdump/`** (untracked, ~2 KB). Ad-hoc Kafka `state.positions` dumper written while diagnosing ghost positions. Kept and committed as gr33d `0cfd965f`.
- **D2 — `/tmp` scratch:** `/tmp/posdump-bin`, `/tmp/posdump/`, `/tmp/ma.o`, `/tmp/mm.o`. Safe to delete. (not yet approved)

### F — Spec acceptance items still open (DISCUSS FIRST)

- **F1 — P2 item 5:** "Run a deterministic replay and a short paper-only integration test." grodt has **no replay capability** (no command, no harness) — this is new work, not a test to add.
- **F2 — P1 fill-attribution test:** acceptance wants "attributable fills update the correct steps exactly once; unrelated historical fills do not falsely satisfy current-run steps." Planner-side fix landed; no test exercises fill attribution specifically.

## Known non-issues

- `TestReconciliationRunDeadlineCancelsInflightRequest` — pre-existing flake, do not "fix".
- `is_simulation: false` in old traces/state snapshots — harmless; inner schema allows extra properties, replaced on next `get_market_state`.

## Working constraints (standing)

- Never `git add config.yaml` (live secrets).
- User + grodt share one LLM — cannot run debug and agent concurrently.
- Sync on items before acting; no unapproved scope.
