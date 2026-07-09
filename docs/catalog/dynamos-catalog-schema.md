# DYNAMOS Catalog: DCAT/ODRL JSON-LD Schema

Tracks [issue #4](https://github.com/DYNAMOS-UVA/DYNAMOS/issues/4), sub-issue of [#2](https://github.com/DYNAMOS-UVA/DYNAMOS/issues/2), building on the field mapping from [#3](https://github.com/DYNAMOS-UVA/DYNAMOS/issues/3) (`docs/catalog/dynamos-data-inventory.md`). Feeds T1.2 (catalog endpoint implementation).

## Implementation reference

- DSP Catalog Protocol: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#catalog-protocol
- DSP Catalog HTTP binding: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#catalog-http

Built directly against the normative JSON Schemas and examples in the spec's source repo (`eclipse-dataspace-protocol-base/DataspaceProtocol`, commit `0b7ac408076e61dbe96bfd72a7676fb847567581`), vendored locally in [`spec-reference/`](./spec-reference/) so this stays reproducible even if the upstream spec changes:
- `spec-reference/catalog/catalog-schema.json`, `dataset-schema.json` — structural requirements
- `spec-reference/catalog/example/catalog.json`, `dataset.json` — reference examples
- `spec-reference/context/dspace.jsonld`, `odrl.jsonld` — exact `@context` term bindings (which properties are `@vocab`-typed, `@id`-typed, etc.)
- `spec-reference/specifications/catalog.protocol.md`, `catalog.binding.https.md` — the prose spec text itself

See [`spec-reference/README.md`](./spec-reference/README.md) for the full file layout and provenance.

## Worked example

`docs/catalog/dynamos-catalog-example.jsonld` — a complete `Catalog` response for the `wageGap` dataset from `configuration/etcd_launch_files/{agreements,datasets}.json`, as VU would serve it to the counterparty `jorrit.stutterheim@cloudnation.nl` under the existing `Relation`.

Validated: syntactically valid JSON, and every field required by `catalog-schema.json`/`dataset-schema.json` is present (`@context`, `participantId`, `@id`, `@type` on the root Catalog; `hasPolicy` + `distribution` on the Dataset; `accessService` + `format` on each Distribution; `@type` + `endpointURL` on the DataService).

## Decisions (resolving the open questions from #3)

### 1. `Dataset.tables` → one `dcat:Distribution` per table

The DSP `Dataset` schema models `distribution` as an array (`minItems: 1`) with no native concept of "tables within a dataset." Rather than inventing a custom property to hold the table list, each DYNAMOS table becomes its own `Distribution` entry, carrying a `dynamos:table` extension property naming it. This fits the existing array shape exactly instead of fighting it — a `wageGap` dataset with 2 tables (`Aanstellingen`, `Personen`) becomes 2 `Distribution` objects.

### 2. `format` does not mean a downloadable file format

DYNAMOS never exposes `wageGap` as a raw CSV download — access always means submitting a request (`sqlDataRequest`/`genericRequest`) through the agent, which runs the archetype-constrained microservice chain and returns computed results. Using a media type like `text/csv` here would misrepresent how the data is actually obtained. Instead, `format` is set to `dynamos:sqlDataRequest` — a custom vocabulary term identifying DYNAMOS's own access mechanism, consistent with `accessService` pointing at the agent's `DataService` endpoint rather than a static file server. `Dataset.delimiter` is preserved as the `dynamos:delimiter` extension property on each `Distribution`, since it's an implementation detail of that access mechanism, not core DCAT metadata.

### 3. `sensitive_columns` is deliberately excluded from the catalog

The runtime `datasets.json` carries a `sensitive_columns` field that has no equivalent in the `pb.Dataset` proto and is not represented anywhere in this JSON-LD document. Column-level sensitivity is an internal enforcement concern handled inside the compute-to-data / TTP execution path, not something a catalog listing (a discovery-time, potentially pre-authentication artifact per the DSP spec's own security notes) should advertise. Exposing which columns are considered sensitive would leak information about the dataset's structure to any party that can query the catalog. If this ever needs to be DSP-visible, the spec's `odrl:duty`/`odrl:prohibition` constructs would be the right place — but that's deferred, not decided here.

### 4. `Relation.ID` becomes the local part of a minted DSP `@id`

The example data's `Relation.ID` is a placeholder (`"GUID"`). Rather than exposing DYNAMOS's internal identifier scheme as-is or inventing a parallel one, the Offer's `@id` is `urn:dynamos:offer:<Agreement.name>:<Relation.ID>` (`urn:dynamos:offer:VU:GUID` here) — DYNAMOS keeps its own `Relation.ID` as the source of truth, and the DSP-facing identifier is a deterministic, namespaced wrapper around it. The same `urn:dynamos:*` scheme is used for `participantId`, `assigner`, and service/dataset `@id`s, so every DSP-facing identifier is traceable back to a DYNAMOS entity without a separate ID-mapping table.

### 5. Archetypes and compute providers → a custom ODRL constraint profile

`Relation.allowedArchetypes` and `Relation.allowedComputeProviders` don't correspond to any term in core ODRL. They're modeled as `constraint` entries on each `permission`, using a small custom vocabulary (`dynamos:archetype`, `dynamos:computeProvider`) as `leftOperand`, ODRL's own `isAnyOf` operator (defined in `context/odrl.jsonld`, so no extension needed there), and the allowed values as `rightOperand`. This is a genuine extension profile, not a repurposing of standard ODRL terms — flagged in [[Data-Sharing Archetypes]] as exactly this kind of open question, now resolved with a concrete shape. `Relation.RequestTypes` maps to one ODRL `permission` per request type (`dynamos:sqlDataRequest`, `dynamos:genericRequest`), each carrying the same archetype/compute-provider constraints, rather than a single permission with an action list — this mirrors the real spec example's shape (one `action` per `permission` object) instead of inventing a multi-valued `action`.

### 6. The catalog is scoped to one counterparty, matching DYNAMOS's `Relation` model

The DSP spec explicitly allows a Catalog to be "dynamically generated for a request based on the requesting Participant's credentials." DYNAMOS's `Relation` is already keyed per counterparty (`Agreement.relations[email]`), so the natural implementation is: **the catalog endpoint generates one Catalog response per requesting participant**, containing only the Datasets/Offers that participant has a `Relation` for. The worked example reflects this — its `@id` and `assignee` are scoped to `jorrit.stutterheim@cloudnation.nl`, not a global catalog of everything VU has.

## Still open (deferred to T1.2)

- The custom vocabulary base URI (`https://dynamos.example/vocab#`) is a placeholder — needs a real, resolvable namespace before implementation.
- Whether `sensitive_columns` needs any DSP-visible representation at all, or stays purely internal (see decision 3).
- How `Agreement`-level (party-wide) `computeProviders`/`archetypes` — as opposed to per-`Relation` grants — surface, if at all, in a catalog response; this document only models the `Relation`-level Offer.
