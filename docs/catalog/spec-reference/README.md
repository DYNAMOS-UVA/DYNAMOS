# DSP Catalog Spec Reference (vendored)

Raw source files from the Eclipse Dataspace Protocol specification, vendored here for offline reference during catalog implementation (T1.2) and to pin the exact version [[docs/catalog/dynamos-catalog-schema.md]] was built against.

**Source**: https://github.com/eclipse-dataspace-protocol-base/DataspaceProtocol
**Commit**: `0b7ac408076e61dbe96bfd72a7676fb847567581` (`main`, 2025-12-04)
**License**: Apache License 2.0 (per the spec document's own copyright notice)
**Rendered spec pages** (what these files are the source of): https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#catalog-protocol and `#catalog-http`

These are not modified from upstream — same content, just fetched flat instead of cloning the whole spec repo. Re-fetch from the commit above (or a newer one, noting the new SHA here) if the spec changes.

## Layout

Mirrors the upstream repo's own directory structure:

```
catalog/
├── catalog-schema.json          # JSON Schema for a Catalog response (artifacts/src/main/resources/catalog/)
├── dataset-schema.json          # JSON Schema for a Dataset (and nested Distribution/DataService)
└── example/
    ├── catalog.json              # Reference example: a Catalog with one Dataset + Offer
    ├── nested-catalog.json       # Reference example: a Catalog referencing a sub-Catalog
    ├── dataset.json              # Reference example: a standalone Dataset response
    ├── catalog-request-message.json
    ├── dataset-request-message.json
    └── catalog-error.json
common/
└── context-schema.json          # JSON Schema constraining the @context value
context/
├── dspace.jsonld                 # Full @context term bindings (Catalog, Dataset, Distribution, DataService, message types)
└── odrl.jsonld                   # ODRL vocabulary context (action, constraint, operator, permission, etc.)
specifications/
├── catalog.protocol.md           # Prose spec: Catalog Protocol (message types, Offer rules)
├── catalog.binding.https.md      # Prose spec: HTTPS binding (endpoints, pagination, compression)
└── terminology.md                # Shared term definitions (Consumer, Provider, Dataspace, etc.) referenced throughout
```

## How `dynamos-catalog-example.jsonld` uses these

- `catalog/catalog-schema.json` and `catalog/dataset-schema.json` define every field DYNAMOS's worked example must satisfy (`@context`, `participantId`, `@id`/`@type`, `hasPolicy`, `distribution`, `accessService`, `format`, etc.) — the worked example was checked against these required-property lists by hand.
- `context/dspace.jsonld` and `context/odrl.jsonld` were the source for exact term semantics used in the decisions doc — e.g. confirming `format` and `leftOperand`/`operator` are `@vocab`-typed (so a custom `dynamos:` prefix resolves correctly), that `assigner`/`assignee`/`participantId` are `@id`-typed (must be IRIs, not plain strings), and that `isAnyOf` is a native ODRL operator (no extension needed for archetype/compute-provider constraints).
- `catalog/example/catalog.json` and `dataset.json` are the shape the worked example follows (one `Offer` per `Dataset`, `Distribution.accessService` as an id-reference or embedded `DataService`).

See `docs/catalog/dynamos-catalog-schema.md` for the actual DYNAMOS-specific mapping decisions built on top of these.
