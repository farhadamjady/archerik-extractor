# Service Discovery Extractor — Design & Decision Log

This is the concrete implementation plan and the decisions behind it. `CLAUDE.md`
holds the terse contract; this document holds the reasoning and the full plan.
Companion background lives in the session memory (auto-loaded).

---

## 0. What this is
A Go CLI that reads ONE service's repo and emits architecture metadata as JSON
(endpoints, outbound deps, Kafka edges, schemas). Deterministic extraction (AST +
config parsing, no LLM inside the extractor). Runs in CI. **Paid/gated** — requires
a per-user API key (see §13), so a run is NOT offline.

## 1. Core principles
- **Code is the source of truth** for request/response structure (Java DTOs), not yml.
- **Honest coverage** — resolve what's resolvable; emit unresolved things as
  `uncertain`/unknown rather than guessing or dropping. Never fabricate.
- **Never drop a node or an edge** — partial + flagged beats missing.
- **Deterministic, byte-stable output** — so the backend can diff commits reliably.

## 2. Architecture — provider seam (framework OVER language)
Single service per repo. Everything stack-specific sits behind a seam; the core
never names a framework.

- **Language layer** (`internal/provider/lang/java`) — shared: tree-sitter Java
  parsing, `TypeIndex`, `SymbolIndex`. A DTO is a DTO regardless of framework.
- **Framework provider** (`internal/provider/spring`, later `micronaut`) — owns
  detection markers, the annotation/keyword **query rules**, and config idioms.
  Declares which language layer it uses.
- Adding **Micronaut** (planned next) = new `provider/micronaut` reusing `lang/java`
  + one line in `internal/registry.Default()`. No change to model/detect/scan/pipeline.
- Detectors look only for THEIR framework's markers (Spring `@RestController`/
  `@FeignClient` vs Micronaut `@Controller`/`@Client`) — fast, targeted scanning.

## 3. Pipeline (phases)
```
auth-gate → detect → collect → parse → index → detect(query engine)
          → schema pass → marshal(deterministic) → submit
```
- **auth-gate** — validate API key (fail-closed). §13
- **detect** — pick the single provider from markers; fail loud on none/ambiguous. §5
- **collect** — walk repo per `FileSpec`, bucket files by `FileKind`.
- **parse** — route each bucket to its parser (parser set keyed by kind). §6
- **index** — build the shared cross-file `Index`. §7
- **detect (query engine)** — one tree-sitter traversal per file dispatches matches
  to detector rules; handlers emit edges, resolving via `Index` + value resolver. §6, §8
- **schema pass** — attach request/response & topic schemas. §10, §12
- **marshal** — deterministic identity + ordering. §14
- **submit** — authenticated POST of the full graph to the backend. §13, §14

## 4. Output contract (the model)
One JSON file per service. Fields: `service_id`, `service_name`, `repository`,
`endpoints`, `outbound_dependencies`, `kafka_producers`, `kafka_consumers`,
`databases_used` (emitted empty for MVP), `config_dependencies`.

Rules: no `inbound_dependencies` (backend derives). Unresolved deps still emitted,
marked unknown/external. Emit raw; backend stores and maps.

**Every communication edge carries three ORTHOGONAL fields** (§9):
- `protocol` — `rest | kafka | grpc | websocket | unknown`
- `detection` — `feign | resttemplate | webclient | kafka | config`
- `confidence` — `confirmed | likely | uncertain`

**Per schema field:** `type`, `nullable`, `required` (tri-state, §11), plus
`items`/`key_type`/`value_type`/`nested`/`truncated`, and `confidence`.

**Planned model additions (not yet in code):** `Protocol` on Endpoint/Dependency/
Kafka edges; `Required` (Requiredness enum) on Schema; `conditional` + `candidate_group`
on edges (§8).

## 5. Detection
Single service per repo, **auto-detect only** (no `--language` override). Provider
`Match(root, fs) -> (matched, score)`. Highest score wins; **fail loud** on zero
matches OR a score tie (ambiguous) — a hard CI failure beats silent misdetection.

## 6. Parsing & traversal
- **Parsers = a set keyed by `FileKind`** (`Java`, `SpringConfig`, `KafkaSchema`).
  `FileSpec` groups globs by kind so the scanner routes each file. (No `OpenAPI`
  kind — OpenAPI is cut, §15.)
- **Traversal = push/query.** Parse each Java file once; detectors declare
  tree-sitter query `Rule`s; ONE traversal per file dispatches matches to handlers.
  A single query captures class-level + method-level annotations together (REST path
  composition = class `@RequestMapping` + method mapping).
- **Java parser = tree-sitter** (`smacker/go-tree-sitter` + Java grammar, cgo).

## 7. Index phase (cross-file, built now)
Shared `Index{ Config, Types, Symbols, Schemas }`, built by Indexers before detectors run:
- `ConfigResolver` — **LAYERED**: (1) Spring application.yml/.properties (+ active profiles);
  (2) **DeployConfig layer** — Helm `values*.yaml` traced through chart-template `env:` blocks,
  rendered K8s ConfigMap/Deployment env, `.env` files; unified by relaxed binding
  (`a.b.c` ≡ `A_B_C`). `${...}` resolution across both layers; deploy-layer hits cap at `likely`;
  divergent overlays → multiple candidates. See §8.5.
- `TypeIndex` — Java DTOs (fields, getters, ctor params, superclass, annotations, imports).
- `SymbolIndex` — constants (`OrderTopics.ORDERS -> "orders"`).
- `SchemaSources` — Kafka schema files (Avro/Proto/JSON-Schema).
**Non-AST facts are read from the Index** (uniform). Indexers own all cross-file /
non-Java parsing; Java query handlers just look values up.

## 8. Value / target resolution (URLs, topics, later gRPC/WS)
Shared, **protocol-agnostic** resolver. Detectors hand it an AST expression node and
get a `ValueSet` back. Recovers **in-code** dynamic targets (hardcoded, concatenated,
conditional) — complements config resolution. Externalized deployment config (Helm
values, rendered K8s env, `.env`) IS resolved via the DeployConfig layer (§8.5); the
residual ceiling is runtime-only sources (Spring Cloud Config Server, secret managers,
values injected in CI and never committed) → those stay `uncertain`.

- **ValueSet lattice:** `Exact{values[]}` | `Template{segments: literals + HOLES}`
  (e.g. `http://{?}/users/{id}`) | `Unknown`; per-value confidence.
- **Evaluator:** literal→Exact; `a+b`/StringBuilder→concat (capped); ternary/switch→
  UNION; local var→reaching definitions (union of all assignments along control paths
  — the "changes by condition" case); constant/enum→SymbolIndex; `@Value("${...}")`→
  ConfigResolver; known builders (UriComponentsBuilder, String.format)→reconstruct;
  method param / unknown call / getenv→HOLE.
- **DECISION — multi-value = ONE EDGE PER CANDIDATE**, tagged `conditional:true` +
  shared `candidate_group`; candidate confidence capped at `likely` (one taken at runtime).
- **DECISION — scope = INTRA-PROCEDURAL** for MVP (params from callers = holes).
  Inter-procedural is a later upgrade.
- **Bounded:** depth cap; candidate-set cap (explosion→Template/Unknown); NO loop
  analysis; memoize; cycle guard; deterministic ordering.
- Code: neutral `ValueSet`+interface in `internal/resolve`; Java evaluator in
  `provider/lang/java`, consumes the Index.

## 8.5 Externalized / deployment config resolution (Helm, K8s, `.env`)
Enterprise Spring services rarely hardcode URLs/topics in `application.yml`; the real
values live in **deployment config** injected as env vars. This is a first-class
resolution SOURCE, not a deferred edge case.

**Resolution chain:** Spring placeholder `${PAYMENT_SERVICE_URL}` → env-var name →
Helm `.Values` path (or K8s ConfigMap key) → literal in `values*.yaml`.

**Sources parsed (new `KindDeployConfig`):**
- Helm: `values.yaml` + overlays `values-<env>.yaml`; chart `templates/**/*.yaml`
  Deployment `env:` blocks (map env-var name → `{{ .Values.x }}`); `Chart.yaml` identifies charts.
- Rendered / plain K8s manifests: ConfigMap `data:`, Deployment `env:` / `envFrom:` (valid YAML).
- `.env` / `*.env` files.

**Two parse modes (Helm templates are NOT valid YAML):**
- Rendered K8s / `.env` / `values.yaml` → real YAML / dotenv parse.
- Helm **templates** → tolerant text scan for `env:` `name:` / `value: {{ .Values.a.b }}`
  pairs, then resolve `.Values.a.b` against merged `values.yaml`(+overlay). We do **NOT** run
  `helm template` (non-deterministic, needs chart deps / network). `_helpers.tpl` named
  templates, `include` / `tpl`, Kustomize patches → best-effort; unresolved → `uncertain`.

**Relaxed binding:** normalize keys so `payment.service.url` ≡ `PAYMENT_SERVICE_URL` ≡
`payment-service-url` ≡ `paymentServiceUrl` unify across layers.

**Overlay selection (mirrors profiles, D3):** `--environment` / `--values` selects overlays;
default = base `values.yaml`. Divergent overlays for one key → **one edge per candidate**
(`conditional` + `candidate_group`), capped `likely`.

**Confidence:** deploy-layer resolution caps at `likely` (extra indirection + overlay
assumption); a value consistent across `application.yml` and the deploy layer may stay
`confirmed`. Unresolvable (runtime / secret / CI-only) → `uncertain`, edge still emitted.

**Bounding / determinism:** same guards as §8 — depth cap on `.Values` chains, cycle guard,
fixed overlay-merge order, deterministic candidate ordering. Topics (Kafka) resolve through
this layer too, not just HTTP URLs.

## 9. Protocol (first-class)
`protocol` is its own edge field, **orthogonal to `detection`**. Feign/RestTemplate/
WebClient are three detections of ONE protocol (`rest`); Kafka producer/consumer are
two detections of `kafka`. On Endpoint, Dependency, and Kafka edges uniformly, so the
graph is queryable by protocol. New protocols (gRPC, WebSocket) = new detectors +
value-resolver adapters. **Interaction style** (sync/async, unary/stream/bidi) is
reserved for later, not modeled now.

## 10. REST schema extraction ladder (dirty-code tolerant)
Resolution ladder with graceful degradation; UNION structure from many sources;
always emit a node. Lives in `internal/schema`, fed by `TypeIndex`; runs in the schema pass.
1. Locate type expr — request = `@RequestBody` param (ignore `@Valid`); response = return type.
2. Data-flow fallback for opaque returns (`ResponseEntity<?>`, raw, `Object`) — inspect
   `return` exprs. (MVP inclusion of this step is a knob — may defer.)
3. Unwrap containers — ResponseEntity/Mono/Flux/Optional/CompletableFuture/Page/Slice →
   inner; List/Set/array → array+items; Map → key/value; unknown generic → unwrap+uncertain;
   wildcard/raw/Object → uncertain.
4. Resolve name→FQN→TypeDef (imports+TypeIndex): repo DTO→walk; scalar→confirmed;
   external→partial; Map<String,Object>/JsonNode→uncertain.
5. Extract fields = UNION of record components, ctor params, declared fields
   (skip static/transient/@JsonIgnore), getters (even without a field), Lombok
   (@Data/@Value → declared fields authoritative). Dedupe by wire name.
6. Jackson wire semantics — @JsonProperty rename, @JsonIgnore drop, @JsonNaming/snake_case.
7. Nullability + requiredness (§11).
8. Merge inherited fields (walk `extends`, stop at Object/external).
9. Recurse nested DTOs, depth-1: **limit 2** (config knob) → deeper `{object,truncated}`;
   cycle detection truncates.
10. Confidence + always-emit: confirmed = repo DTO/scalar fully extracted; likely = one
    indirection; uncertain = dynamic/unresolved. Total failure still emits `{object,uncertain}`.

## 11. Nullability & requiredness (two orthogonal axes)
- **Nullable** (may value be null): primitive non-null; Optional nullable;
  @NotNull/@NonNull non-null; @Nullable/@JsonInclude(NON_NULL) nullable; else unset.
- **Required** (must field be present) — **tri-state `required|optional|unknown`,
  default `unknown`**, always emitted (no omitempty) so backend tells "unknown" from
  "optional". Required signals: @NotNull/@NotBlank/@NotEmpty, @JsonProperty(required=true),
  primitive, record/no-default ctor param, @RequestBody/@RequestParam default required.
  Optional signals: @Nullable, @JsonProperty(required=false), Optional<T>, default
  initializer, @RequestParam(required=false)/defaultValue, @JsonInclude(NON_NULL).
  Well-signaled on REQUEST DTOs; often `unknown` on responses (honest, not failure).

## 12. Kafka schema strategy (files-first, inverts REST priority)
Edge and schema are detected independently — the producer/consumer→topic edge comes
from code and is ALWAYS emitted if real; schema is enrichment on top.
- **K1** locate payload type + topic (unwrap ConsumerRecord/Message/List batch;
  reuse REST unwrapper; resolve topic via config).
- **K2** wire format from config serializer/deserializer (Avro/Proto/JsonSchema/
  Spring-Json/String).
- **K3** resolve files-first: (1) schema file in repo matched by Avro name+namespace /
  Proto message / filename → confirmed; (2) in-code DTO → run the REST Walker → likely;
  (3) Schema Registry — DEFERRED (network/creds); (4) safe-fail.
- **DECISION — safe-fail = KEEP THE EDGE, DROP ONLY THE SCHEMA**:
  `{topic, resolved:false, schema:null, confidence:uncertain}`. Only truly false edges
  (test/generated/commented) are skipped (exclusions). Consistent with the HTTP rule.
- Nullability/requiredness come natively from files (Avro union[null,T]→nullable,
  no-default→required; JSON-Schema required[]+type:null; Proto3 repeated/optional).

## 13. Access & licensing (the tool is NOT free)
Per-user **API key** gates access.
- **Startup phone-home gate (before any scan):** resolve key, call validate API.
  Invalid/missing/quota/unreachable → exit non-zero, run nothing. **FAIL-CLOSED** on
  unreachable (no offline grace) — accepted tradeoff: a validation-server outage breaks
  customer CI, so that endpoint needs strong uptime.
- **No free local-only** — even producing JSON requires a valid key.
- **Key precedence:** `--api-key` > `EKG_API_KEY` env > config file. Never logged.
- **Real enforcement is server-side:** the key also travels with **submission**, where
  the backend re-validates. Startup check is fail-fast UX + soft deterrent (a local
  binary's check is bypassable). Wire both.
- **Out of this repo:** key issuance, accounts, metering, quotas, billing = a separate
  control-plane backend. The extractor only carries/validates/submits with the key.

## 14. Incremental / per-commit
Each commit runs a **FULL scan of the single service** and emits the COMPLETE graph —
NOT a scan of only changed files (a commit's impact ≠ its changed files, due to
cross-file resolution; deletes must remove nodes). "Changes only" is computed at the
**backend** by diffing the new full graph vs the stored one. Driver was CI/scan speed —
served instead by single-service scans being fast + parallel parsing + scoped globs +
optional content-hash parse cache. **Requires deterministic identity + byte-stable
output:** endpoint = verb + composed path; dependency = target_name + detection;
kafka edge = topic + direction; schema tied to owner; marshal sorts deterministically.

## 15. Scope (MVP)
**In:** Spring Boot (Java) + Kafka. REST endpoints (@RestController), HTTP clients
(Feign/RestTemplate/WebClient), Kafka producers/consumers, config placeholder resolution,
**externalized deployment-config resolution (Helm values + K8s/`.env`, static best-effort, §8.5)**,
code-derived schemas, value resolution, protocol tagging.
**Cut/deferred:** DB detection (JPA/JDBC) · OpenAPI/Swagger ingestion · Kafka Schema
Registry access · gRPC · WebSocket · full K8s workload/topology parsing · running
`helm`/`kustomize` (we trace statically) · Spring Cloud Config Server + secret managers (runtime) ·
LLM · version history · schema versioning · inter-procedural value analysis · interaction-style modeling.
**Planned next:** Micronaut framework.

## 16. Build order
1. **Provider-seam rewrite** — FileKind, grouped FileSpec, `Parsers()`, `Indexers()`,
   rule-based `Detector`, `Index`, `ScanContext`; add `Protocol` to the model +
   deterministic node identity. (Replaces current stubs; makes it build.)
2. **Pipeline shell** — phases wired + `registry` + `cmd/extractor/main.go` emitting
   valid empty JSON end-to-end; auth-gate + submit stubs.
3. **tree-sitter Java parser + QueryEngine** — one query running against a real file.
4. **Config indexer + ConfigResolver** — Spring application.yml/.properties + active profiles;
   chained placeholder resolution (cap 10).
4b. **DeployConfig indexer (§8.5)** — Helm `values*.yaml` + chart-template `env:` trace, K8s
    ConfigMap/Deployment env, `.env`; relaxed-binding bridge; overlay candidates. Extends the
    same layered ConfigResolver.
5. **Value resolver** (`internal/resolve` + Java evaluator).
6. **Detectors, one at a time:** REST → Feign → RestTemplate → WebClient → Kafka.
7. **TypeIndex/SymbolIndex + schema pass** (REST ladder + Kafka files-first).
8. **Auth + submit** wiring.

## 17. Tooling / environment
- Go 1.26.4 (installed via Homebrew), Apple Silicon, Apple clang 17 present (cgo OK).
- Module: `github.com/farhadamjady/super-discovery` (note: repo is `service-discovery`;
  module path may be renamed).
- Current code is a **pre-rewrite skeleton that does not build yet** (Spring provider
  references detectors not yet written). Step 1 fixes this.

## 18. Coverage expectations (priors, not measured)
Idiomatic Spring MVC: ~85–95% of edges/endpoints found, ~75–85% confidently resolved.
Platform-heavy (WebFlux functional routing, Spring Cloud Stream): ~50–65%. Externalized
config that lives in Helm / K8s / `.env` is now **recovered** (§8.5), moving the ceiling up;
the residual ceiling is runtime-only config (Config Server, secrets, CI-injected values).
Highest-leverage later additions: meta-annotation resolution, Spring Cloud Stream,
WebFlux functional routing. Recommended: build a small labeled benchmark (PetClinic +
Spring Cloud/Kafka samples) to turn priors into measured precision/recall.

## 19. Decision quick-reference
| Topic | Decision |
|---|---|
| Repo shape | Single service per repo |
| Stack detection | Auto-detect only; fail loud on none/ambiguous |
| Seam | Framework over shared language layer; Spring now, Micronaut next |
| Traversal | Push/query (tree-sitter), one pass per file |
| Parsers | Set keyed by FileKind (Java, SpringConfig, KafkaSchema) |
| Cross-file index | Built now (Config, Types, Symbols, Schemas) |
| Non-AST facts | Read from the Index |
| Schema source | Code-authoritative (DTO); OpenAPI cut |
| Requiredness | Tri-state required/optional/unknown, default unknown |
| Kafka schema | Files-first; safe-fail keeps edge, drops schema |
| Schema Registry | Deferred |
| DB detection | Deferred |
| Value resolution | Intra-procedural; one edge per candidate |
| Externalized config | Helm/K8s/`.env` resolved statically, best-effort (§8.5); runtime/secrets deferred |
| Protocol | First-class field, orthogonal to detection; style deferred |
| Access | Paid; per-user API key; phone-home fail-closed; no free local-only |
| Incremental | Full scan + delta at backend; deterministic identity |
