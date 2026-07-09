# DYNAMOS Data Source Inventory for the Catalog

## Implementation reference

- DSP Catalog Protocol: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#catalog-protocol
- DSP Catalog HTTP binding: https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#catalog-http

## Summary

An "offer-able" data exchange job in DYNAMOS is the combination of:

1. **What data exists** — a `Dataset` (proto message, served over etcd via `Etcd.GetDatasetMetadata`).
2. **Under what conditions it can be requested** — a `Relation` between two parties, scoped inside that party's `Agreement`.

Together these map onto a DSP `dcat:Dataset` carrying one or more ODRL policy `Offer`s: the `Dataset` supplies the DCAT resource description, and the `Relation`/`Agreement` fields supply the ODRL policy constraints and parties.

## Source locations

| Struct                       | File                                               |
| ---------------------------- | -------------------------------------------------- |
| `pb.Dataset` (proto)         | `proto-files/etcd.proto`                           |
| `Relation`, `Agreement` (Go) | `go/pkg/api/http.go:64-77`                         |
| Example dataset data         | `configuration/etcd_launch_files/datasets.json`    |
| Example agreement data       | `configuration/etcd_launch_files/agreements.json`  |
| Request type definitions     | `configuration/etcd_launch_files/requestType.json` |

## Field mapping

### `pb.Dataset` → `dcat:Dataset`

| DYNAMOS field       | Type              | DCAT/ODRL equivalent                                                                            | Notes                                                                         |
| ------------------- | ----------------- | ----------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `Dataset.name`      | `string`          | `dcat:Dataset` title (`dct:title`)                                                              | e.g. `"wageGap"`                                                              |
| `Dataset.type`      | `string`          | `dcat:mediaType` / `dct:format` on the `dcat:Distribution`                                      | e.g. `"csv"`                                                                  |
| `Dataset.delimiter` | `string`          | Distribution-level format detail, not a core DCAT property                                      | CSV-specific; likely a custom extension property on the `Distribution`        |
| `Dataset.tables`    | `repeated string` | Either multiple `dcat:Distribution`s (one per table) or a custom property listing sub-resources | Needs a decision in #4 — DCAT has no native "tables within a dataset" concept |

Note: the runtime JSON for a dataset (`configuration/etcd_launch_files/datasets.json`) also carries a `sensitive_columns` field not present in the proto — worth flagging for #4 since it affects what can safely be exposed in a public catalog listing.

### `Agreement` → catalog scope / assigner

| DYNAMOS field                | Type                  | DCAT/ODRL equivalent                                                               | Notes                                                                           |
| ---------------------------- | --------------------- | ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| `Agreement.name`             | `string`              | ODRL `assigner` (the offering party), or the `dcat:Catalog` owner                  | e.g. `"VU"`, `"UVA"`                                                            |
| `Agreement.relations`        | `map[string]Relation` | Collection of ODRL `Offer`s scoped per counterparty                                | Keyed by counterparty identity (e.g. email) — maps to ODRL `assignee` per Offer |
| `Agreement.computeProviders` | `[]string`            | Available `assigner`/`assignee` parties for `computeToData`-style ODRL constraints | Party-level allow-list, broader than a single Relation                          |
| `Agreement.archetypes`       | `[]string`            | Enumerates which ODRL constraint "shapes" this party supports                      | Superset of `Relation.allowedArchetypes`                                        |

### `Relation` → ODRL `Offer`

| DYNAMOS field                      | Type       | DCAT/ODRL equivalent                                                                    | Notes                                                                       |
| ---------------------------------- | ---------- | --------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| `Relation.ID`                      | `string`   | ODRL Offer/Policy `@id`                                                                 | Currently a placeholder value (`"GUID"`) in example data                    |
| `Relation.RequestTypes`            | `[]string` | ODRL `action` on the Offer                                                              | e.g. `"sqlDataRequest"`, `"genericRequest"` — defined in `requestType.json` |
| `Relation.DataSets`                | `[]string` | Links the Offer to the `dcat:Dataset` it applies to (by `Dataset.name`)                 | This is the join key between `Relation` and `Dataset`                       |
| `Relation.AllowedArchetypes`       | `[]string` | ODRL `constraint` on the Offer (permitted execution pattern)                            | e.g. `"computeToData"`, `"dataThroughTtp"`                                  |
| `Relation.AllowedComputeProviders` | `[]string` | ODRL `constraint` / `assigner` restriction (which compute provider may execute the job) | e.g. `"SURF"`                                                               |

## Open questions for #4 (schema design)

- How to represent `Dataset.tables` in DCAT — separate `Distribution`s vs. a custom property.
- Whether `sensitive_columns` (present in the runtime JSON but not the proto) should be excluded from the catalog entirely or represented as an ODRL `duty`/`prohibition`.
- Whether `Relation.ID` becomes the real ODRL Policy identifier or DYNAMOS keeps its own ID and mints a separate DSP-facing identifier.
- How archetypes (`computeToData`, `dataThroughTtp`, `reproducableScience`) map onto ODRL's constraint vocabulary — likely needs a custom ODRL profile/extension rather than reusing core ODRL terms directly.
