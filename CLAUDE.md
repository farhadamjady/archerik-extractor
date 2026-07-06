# CLAUDE.md — Engineering Knowledge Graph: Extractor

## What this is
A Go tool that reads a repo and outputs architecture metadata as JSON.
Runs in CI or locally. **Deterministic only — AST + config parsing, NO LLM inside the extractor.**
Part of an Engineering Knowledge Graph MVP: static architecture intelligence from source code.

**Paid / gated.** The extraction *logic* is deterministic, but a run is NOT offline: it
requires a per-user **API key** and network — see "Access & licensing" below.

## Scope
- Languages/frameworks: **Spring Boot (Java), Kafka.** Nothing else. (Micronaut planned next framework.)
- gRPC: later milestone, not now.
- **Externalized deployment config IS in scope** as a placeholder-resolution SOURCE (static,
  best-effort): Helm (`values*.yaml` + chart-template `env:` blocks), rendered K8s manifests
  (ConfigMap/Deployment env), and `.env`-style files. Big-company Spring services read URLs/topics
  from here, not from `application.yml` — see "Coverage rules".
- Cut from MVP: **DB detection (JPA/JDBC), OpenAPI/Swagger ingestion**, full K8s workload/topology
  parsing, **running `helm`/`kustomize`** (we trace statically, never render), Spring Cloud Config
  Server + secret managers (runtime sources), LLM for ambiguous cases, version history, schema versioning.

## Access & licensing
The tool is **not free** — gated by a per-user **API key**.
- **Startup gate (before any scanning):** resolve key, call the validation API. Invalid/missing/
  quota-exceeded/unreachable → exit non-zero, run nothing. **Fail-closed** when the server is
  unreachable (no offline grace).
- **No free local-only mode** — even producing JSON requires a valid key.
- **Key input precedence:** `--api-key` > `EKG_API_KEY` env (primary CI-secret path) > config file.
  Never logged; masked in diagnostics.
- **Real enforcement is server-side:** the key also travels with results **submission**, where the
  backend re-validates. A phone-home check in a local binary is bypassable — the submit-time check
  is the robust gate; the startup check is fail-fast UX + soft deterrent. Wire both.
- **Out of this repo:** key issuance, accounts, metering, quotas, billing = a separate control-plane
  backend. The extractor only carries, validates, and submits with the key.
- **Pipeline with auth:** auth-gate → detect → collect → parse → index → detect → schema pass →
  marshal → submit (authenticated POST to the ingest API).

## Output
One JSON file per service. Fields:
`service_id`, `service_name`, `repository`, `endpoints`, `outbound_dependencies`,
`kafka_producers`, `kafka_consumers`, `databases_used`, `config_dependencies`.

Each edge carries three ORTHOGONAL fields:
- **protocol** (rest / kafka / grpc / websocket / unknown) — the communication protocol. First-class;
  Feign/RestTemplate/WebClient are three detections of ONE protocol (rest). Important for the future.
- **detection method** (Feign / RestTemplate / WebClient / Kafka / config) — how we found it in code.
- **confidence** (confirmed / likely / uncertain).
On `endpoints`, `outbound_dependencies`, and Kafka edges alike, so the graph is queryable by protocol.
Interaction style (sync/async, stream/bidi) is reserved for later, not modeled yet.

Rules:
- Do NOT emit `inbound_dependencies` — backend derives those.
- Unresolved deps are still emitted, marked unknown/external.
- Emit raw. Backend stores and maps.

## Per-commit / incremental
Each commit runs a **full scan of the (single) service** and emits the COMPLETE, correct graph —
NOT a scan of only the changed files. Rationale: a commit's architectural impact ≠ its changed
files (config placeholder / DTO / constant edits ripple into edges in unchanged files; deletes must
remove nodes). Scanning only touched files misses the blast radius → stale graph.
- **"Changes only" is computed at the backend** by diffing the new full graph vs the stored version
  (upserts/deletes + per-commit delta view). Extractor stays stateless; no cross-commit index.
- **CI/scan speed** (the reason to want incrementality) is served instead by: single-service scans
  being fast, parallel parsing, scoped globs/exclusions, optional content-hash parse cache.
- **Determinism required for reliable backend diffing:** stable node/edge identity + stable output
  ordering (byte-stable). Keys: endpoint = verb + composed path; dependency = target_name +
  detection; kafka edge = topic + direction; schema tied to its owner.

## What to extract per service
- REST endpoints: `@RestController` + mapping annotations.
- HTTP clients out: FeignClient, RestTemplate (`getForObject`/`exchange`/etc.), WebClient.
- Kafka producers (`KafkaTemplate.send`) + consumers (`@KafkaListener`).
- DB usage: JPA (`@Entity`/`@Repository`), JDBC datasource config. **(deferred — post-MVP)**

## Coverage rules (the hard part)
The challenge is NOT finding annotations. It's that targets are rarely hardcoded.

1. **Config indirection** — biggest source of misses. URLs/topics are usually placeholders:
   `@FeignClient(url="${payment.service.url}")`, `@KafkaListener(topics="${orders.topic}")`.
   Resolve through a **LAYERED** config source BEFORE emitting an edge:
   (a) Spring `application.yml`/`.properties` (+ active profiles);
   (b) **externalized deployment config** — Helm `values*.yaml` traced through chart-template
   `env:` blocks (`{{ .Values.x }}` → env-var name → Spring property), rendered K8s
   ConfigMap/Deployment env, and `.env` files;
   unified by **relaxed binding** (`payment.service.url` ≡ `PAYMENT_SERVICE_URL`). A value found
   only in the deploy layer caps at `likely`; divergent env overlays (staging vs prod) → one edge
   per candidate. Can't resolve (runtime/secret/CI-injected) → unknown/external, confidence = uncertain.

2. **REST path composition** — endpoint path = class-level `@RequestMapping` + method-level
   `@GetMapping`/`@PostMapping`/etc. Concatenate both. Keep the HTTP verb. Preserve path
   variables (`/users/{id}`). Never emit method paths in isolation.

3. **Confidence = detection certainty, not source type:**
   - confirmed = literal value found (hardcoded URL/topic, resolved placeholder)
   - likely = resolved through config with one indirection
   - uncertain = dynamic/computed (string concat, variable, builder) — still emit a node, flag it

4. **Service-name resolution** — `@FeignClient(name="payment-service")` is a logical name,
   not a service_id. Emit the raw name. Backend maps name → service_id. Don't guess the mapping.

5. **Exclusions** — skip `src/test`, generated code, commented-out code. They inflate the graph
   with false edges.

6. **Honest coverage** — capture resolvable dependencies well, not 100%. Unresolvable dynamic
   cases become uncertain/unknown nodes. That's correct behavior, not failure. Honesty keeps
   the graph trustworthy.

## Schema extraction (declared contract per endpoint/topic)
Capture the **declared structure** (static), not runtime payloads.
No versioning — store current only, overwrite on change.
Each endpoint and each topic gets an attached schema object: `name`, `type`, `nullable`, `nested`.

### REST schema source order
**The code is the source of truth.** The Java DTO in the source is authoritative for
request/response structure. OpenAPI ingestion is cut from the MVP.
1. Parse Java DTO referenced by the controller method (request param type, return type) → confirmed
2. Can't resolve type → uncertain, store partial (via the dirty-code resolution ladder)

### Kafka schema source order (vendor-neutral — do NOT hardwire to Confluent)
1. Schema Registry if configured (Confluent, Apicurio, AWS Glue — Avro/Protobuf, by subject) → confirmed
2. Schema files in repo (`.avsc`, `.proto`, JSON Schema — service repo or shared contracts repo) → confirmed
3. In-code DTO / value class → likely
4. Unresolved → uncertain, partial
Tie each schema to its topic + producer/consumer.

### Resolved decisions
- **Nested DTO walk depth = 2 levels.** Then stop; mark deeper fields as
  `{"type": "object", "truncated": true}`. Make depth a config knob.
- **Generics / collections — record container + inner type:**
  - `List<User>` → `{"type": "array", "items": "User"}`
  - `Map<String, Order>` → key type + value type
  - `Optional<String>` → `String` with `nullable: true`
  - `Page<Invoice>` and similar wrappers → unwrap to inner type
  - Unknown generic → flag uncertain
- **Schema Registry access** — run the extractor on the pipeline (CI, next to services) so it
  reuses existing creds + network. For schema-files-in-repo, no network needed — they're in the checkout.

## Build order
1. Project skeleton + JSON output schema (the contract everything feeds into)
2. Config parser + placeholder resolver
3. Detectors, one at a time (Feign → RestTemplate → WebClient → Kafka) — DB deferred post-MVP
4. Schema extraction (DTO → Kafka registry → schema files) — OpenAPI cut

## Still open (decide when reached)
- Confidence rules per detection pattern (exact mapping)
- How deep to resolve nested/chained property placeholders
- Parse multiple Spring profiles or just default
