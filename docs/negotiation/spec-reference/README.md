# DSP Contract Negotiation Spec Reference (vendored)

Raw source files from the Eclipse Dataspace Protocol specification, vendored here for offline reference during contract negotiation study/implementation (T2.1+) and to pin the exact version [[docs/negotiation/dsp-negotiation-state-machine.md]] was built against.

**Source**: https://github.com/eclipse-dataspace-protocol-base/DataspaceProtocol
**Commit**: `0b7ac408076e61dbe96bfd72a7676fb847567581` (`main`, 2025-12-04) - same commit as `docs/catalog/spec-reference/`
**License**: Apache License 2.0 (per the spec document's own copyright notice)
**Rendered spec pages** (what these files are the source of): https://eclipse-dataspace-protocol-base.github.io/DataspaceProtocol/2025-1/#contract-negotiation-protocol and `#negotiation-http`

These are not modified from upstream - same content, just fetched flat instead of cloning the whole spec repo. Re-fetch from the commit above (or a newer one, noting the new SHA here) if the spec changes.

## Layout

Mirrors the upstream repo's own directory structure:

```
negotiation/
├── contract-negotiation-schema.json                     # JSON Schema for the ContractNegotiation resource (the ACK/state object)
├── contract-negotiation-error-schema.json               # JSON Schema for the ContractNegotiationError response
├── contract-request-message-schema.json                 # JSON Schema: Consumer -> Provider, initiate/counter-offer
├── contract-offer-message-schema.json                    # JSON Schema: Provider -> Consumer, initiate/counter-offer
├── contract-agreement-message-schema.json                # JSON Schema: Provider -> Consumer, carries the Agreement
├── contract-agreement-verification-message-schema.json  # JSON Schema: Consumer -> Provider, verifies the Agreement
├── contract-negotiation-event-message-schema.json        # JSON Schema: ACCEPTED (Consumer) / FINALIZED (Provider)
├── contract-negotiation-termination-message-schema.json  # JSON Schema: either party, -> TERMINATED
├── contract-schema.json                                  # JSON Schema for Policy/Offer/Agreement (ODRL), same family as catalog's Offer
└── example/
    ├── contract-negotiation.json                         # Reference example: the ACK/state object (GET response)
    ├── contract-negotiation-error.json
    ├── contract-request-message_initial.json             # Initiating request (no providerPid yet)
    ├── contract-request-message.json                      # Counter-request (has providerPid)
    ├── contract-offer-message_initial.json                # Initiating offer (no consumerPid yet)
    ├── contract-offer-message.json                        # Counter-offer (has consumerPid)
    ├── contract-agreement-message.json
    ├── contract-agreement-message-full.json               # Same, with a fully expanded Agreement/Offer
    ├── contract-agreement-verification-message.json
    ├── contract-negotiation-event-message.json
    └── contract-negotiation-termination-message.json
common/
└── context-schema.json                                   # JSON Schema constraining the @context value
context/
├── dspace.jsonld                                          # Full @context term bindings (same file catalog vendors)
└── odrl.jsonld                                            # ODRL vocabulary context (action, constraint, operator, permission, etc.)
specifications/
├── contract.negotiation.protocol.md                       # Prose spec: Contract Negotiation Protocol (states, message types)
├── contract.negotiation.binding.https.md                  # Prose spec: HTTPS binding (provider + consumer endpoint sets)
├── terminology.md                                          # Shared term definitions (Consumer, Provider, Agreement, etc.)
└── figures/
    ├── contract.negotiation.state.machine.png              # The state machine diagram referenced by the protocol doc
    └── contract.negotiation.state.machine.puml              # Its PlantUML source
```

`context/` and `common/context-schema.json` are duplicated from `docs/catalog/spec-reference/` (same upstream files, same commit) so this directory stays self-contained rather than reaching across into the catalog vendoring.

## How `dsp-negotiation-state-machine.md` uses these

- `negotiation/contract-negotiation-schema.json` and the message schemas define every field each of the 6 message types must satisfy.
- `specifications/contract.negotiation.protocol.md` is the source for the state list, state machine diagram, and message-type semantics (resulting state, sent-by, required properties).
- `specifications/contract.negotiation.binding.https.md` is the source for both the provider and consumer HTTPS path bindings.
- `negotiation/example/*.json` are the worked message shapes referenced when mapping DYNAMOS concepts onto each message.

See `docs/negotiation/dsp-negotiation-state-machine.md` for the actual DYNAMOS-specific mapping.
