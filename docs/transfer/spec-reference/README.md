# DSP Transfer Process Spec Reference (vendored)

Raw source files from the Eclipse Dataspace Protocol specification, vendored here for offline reference during transfer process study/implementation (T3.1+) and to pin the exact version [[docs/transfer/dsp-transfer-state-machine.md]] was built against.

**Source**: https://github.com/eclipse-dataspace-protocol-base/DataspaceProtocol
**Commit**: `0b7ac408076e61dbe96bfd72a7676fb847567581` (`main`, 2025-12-04) - same commit as `docs/catalog/spec-reference/` and `docs/negotiation/spec-reference/`
**License**: Apache License 2.0 (per the spec document's own copyright notice)
**Rendered spec pages** (what these files are the source of): https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#transfer-protocol and `#transfer-http`

These are not modified from upstream - same content, just fetched flat instead of cloning the whole spec repo. Re-fetch from the commit above (or a newer one, noting the new SHA here) if the spec changes.

## Layout

Mirrors the upstream repo's own directory structure:

```
transfer/
├── transfer-schema.json                        # Base JSON Schema shared by transfer messages
├── transfer-error-schema.json                  # JSON Schema for the TransferError response
├── transfer-process-schema.json                # JSON Schema for the TransferProcess resource (the ACK/state object)
├── transfer-request-message-schema.json        # JSON Schema: Consumer -> Provider, initiate a Transfer Process
├── transfer-start-message-schema.json          # JSON Schema: Provider -> Consumer (initial start) or Consumer -> Provider (resume after suspend)
├── transfer-suspension-message-schema.json     # JSON Schema: either party, -> SUSPENDED
├── transfer-completion-message-schema.json     # JSON Schema: either party, -> COMPLETED
├── transfer-termination-message-schema.json    # JSON Schema: either party, -> TERMINATED
├── data-address-schema.json                    # JSON Schema for the DataAddress object (push/pull transport details)
└── example/
    ├── transfer-process.json                   # Reference example: the ACK/state object (GET response)
    ├── transfer-error.json
    ├── transfer-request-message.json
    ├── transfer-start-message.json
    ├── transfer-suspension-message.json
    ├── transfer-completion-message.json
    └── transfer-termination-message.json
common/
└── context-schema.json                         # JSON Schema constraining the @context value
context/
├── dspace.jsonld                                # Full @context term bindings (same file catalog/negotiation vendor)
└── odrl.jsonld                                  # ODRL vocabulary context (unused by transfer messages directly, kept for parity with the other two vendored sets)
specifications/
├── transfer.process.protocol.md                 # Prose spec: Transfer Process Protocol (states, message types)
├── transfer.process.binding.https.md            # Prose spec: HTTPS binding (provider + consumer endpoint sets)
└── figures/
    ├── transfer-process-state-machine.png       # The state machine diagram referenced by the protocol doc
    ├── transfer-process-state-machine.puml       # Its PlantUML source
    ├── push-transfer-process.{png,puml}          # Push transfer sequence illustration
    └── pull-transfer-process.{png,puml}          # Pull transfer sequence illustration
```

No shared `specifications/terminology.md` file exists at this commit for the transfer section (unlike catalog/negotiation) - term definitions (`Provider`, `Consumer`, `Agreement`, `Dataset`) live in the catalog/negotiation vendoring already checked into this repo; not duplicated a third time.

`context/` and `common/context-schema.json` are duplicated from `docs/catalog/spec-reference/` and `docs/negotiation/spec-reference/` (same upstream files, same commit) so this directory stays self-contained rather than reaching across into the other vendored sets.

## How `dsp-transfer-state-machine.md` uses these

- `transfer/transfer-process-schema.json` and the message schemas define every field each of the 5 message types must satisfy.
- `specifications/transfer.process.protocol.md` is the source for the state list, state machine diagram, and message-type semantics (resulting state, sent-by, required properties).
- `specifications/transfer.process.binding.https.md` is the source for both the provider and consumer HTTPS path bindings, including the Provider-initiated-Start vs Consumer-resume-Start asymmetry.
- `transfer/example/*.json` are the worked message shapes referenced when mapping DYNAMOS concepts onto each message.

See `docs/transfer/dsp-transfer-state-machine.md` for the actual DYNAMOS-specific mapping.
