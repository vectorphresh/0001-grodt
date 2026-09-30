# Offline JSON Schema resources

These unmodified official meta-schemas are vendored from
https://github.com/json-schema-org/json-schema-spec on 2026-09-28:

- Draft 2019-09: tag `draft-handrews-json-schema-02`, commit
  `38e1606d71ee848c6cf0490c3457681220c01663`.
- Draft 2020-12: tag `draft-bhutton-json-schema-01`, commit
  `add836e705c9a07434c467b6b90946ba45258a73`.

Each draft directory preserves the upstream `schema.json` and `meta/*.json`
paths for the selected resources. Each document's `$id` is its canonical URI.
The exact URI allowlist in `metaschemas.go` resolves only these embedded bytes;
there is no network fallback. Older draft meta-schemas are bundled by the library.

To update, retrieve the original documents from an official pinned release,
preserve their bytes, update this provenance and the explicit URI map, and run
the validation tests. These documents are data, not GRODT schema semantics.
