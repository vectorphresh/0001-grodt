# Gathering milestone fixture

This import-free `grodt.state/v1` module is a disposable test fixture. It consumes
only `runtime` events whose source ID is `gathering-environment`. Accepted LLM
actions, unrelated events, completion requests, and action rejections are ignored.
Discovery, movement, and gathering propose RFC 6902 patches; the normal Store
validator decides whether to commit them. The fixture never makes host requests.

The host supplies all authoritative current state. Every call uses a fresh WASM
instance. The bounded token-span reader selects object fields and never interprets
text inside unrelated payloads as events. It handles this fixture's lowercase
location identifiers and integer inventory, not a general module-authoring SDK.
Its input is valid JSON serialized by the host; final value shape and ranges are
checked by `schema.json` after each patch.

Check in `gathering.c` and `gathering.wasm` together. Ordinary Go tests load the
binary directly and do not need a compiler. Rebuild from the repository root:

```sh
clang --target=wasm32 -O2 -nostdlib -fno-builtin \
  -fuse-ld=/usr/bin/wasm-ld-15 -Wl,--no-entry -Wl,--max-memory=67108864 \
  internal/state/wasm/testdata/gathering/gathering.c \
  -o internal/state/wasm/testdata/gathering/gathering.wasm
```

`state.json` loads the schema, initial value, and binary using the normal manifest
loader. The scenario and its deterministic environment live in
`cmd/grodt/feedback_test.go`; there is no new CLI mode or external environment API.
