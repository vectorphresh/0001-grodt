# Grodt

Grodt runs a synchronous provider chain from one initial command-line prompt.
The generic provider produces an ordinary answer, then the application makes a
separate schema-constrained goal evaluation through `RequestMutation`. A valid
negative evaluation supplies guidance for the next autonomous cycle; a positive
evaluation ends the run. No stdin input is read.

```sh
go build -o /tmp/grodt ./cmd/grodt
/tmp/grodt "Return the exact token GRODT-7319 and nothing else."
/tmp/grodt --trace --config configs/local.yaml "Explain the purpose of this project."
```

Command shape: `grodt [--trace] [--config path] <prompt>`. Quote the prompt as one
argument and put options before it. Configuration defaults to `config.yaml`.
Use `--` before a prompt that starts with `-`. Do not put credentials in prompts
or command arguments.

Supply `OPENAI_API_KEY` through the process environment. The explicit YAML file
can provide `openai.environment.OPENAI_BASE_URL`. Both exact keys resolve through
`internal/config`, YAML first and then OS environment. Blank YAML values do not
fall through. The configuration file must exist.

The stable application objective is: "Complete the user's request using the
information supplied in this run." It is separate from the initial request and
temporary context. Providers do not own or change this objective.

Each cycle performs two synchronous LLM calls:

1. `Prompt` generates the current answer through `GenericProvider`.
2. `RequestMutation` evaluates the original objective/request against current
   response and context, returning `achieved` and a nonblank rationale.

The evaluation JSON is decoded, never applied as a mutation. The application
currently trusts the model's judgment. A negative evaluation adds the response
and rationale as temporary guidance for another cycle, without asking a human.
A failed generation skips evaluation; a failed/invalid evaluation cannot trigger
continuation. There are no automatic retries or detached provider operations.

The private limit is **20 cycles** (normally at most 40 LLM calls). This is an
execution guard, not a retry count. Temporary context can grow across those
cycles and is not a persistent or bounded-memory strategy. No SKILL.state, MCP,
tools, or world-state mutation are implemented.

Final response goes to stdout. Progress, objective, status, rationale, and metrics
go to stderr. `--trace` additionally shows the generation prompt and structured
evaluation inputs/schema. It includes user-supplied content; credentials and raw
SDK envelopes are excluded, and configured-key occurrences are redacted.

Exit codes:

- **0:** achieved, or cancelled by SIGINT/SIGTERM (`Status` distinguishes them).
- **2:** cycle limit reached without achievement (`Status: incomplete`).
- **1:** configuration, timeout, provider, evaluation, or output failure.

On the cycle limit, the last successfully evaluated response/rationale is
reported. Failed cycles never publish partial responses or infer completion.
SIGINT/SIGTERM cancel the active request. Each LLM operation retains Component
001's two-minute default timeout; there is no additional whole-run timeout.

Requests counts LLM client invocations, including failed calls. Token counts come
only from available authoritative API metadata on successful calls, covering
both generation and evaluation. Reports show usage coverage, `unavailable` when
none is available, and `known subtotal` for partial coverage. No token estimates
or fabricated zeros are used. Component 001 returns `TextResult` or `JSONResult`,
each carrying an optional `*Usage` for that response.

Verification:

```sh
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

Live acceptance is opt-in and requires an already-running llama.cpp service plus
`OPENAI_BASE_URL` and `OPENAI_API_KEY` in the test process environment:

```sh
go test -tags live ./cmd/grodt -run '^TestLive' -count=1 -v
go test -tags live ./internal/openai -run '^TestLive' -count=1 -v
```

The application live tests cover one-cycle completion and a two-cycle
`DRAFT` → evaluation guidance → `GRODT-7319` run. The latter supplies a test
objective through the private application helper; no objective CLI flag exists.
