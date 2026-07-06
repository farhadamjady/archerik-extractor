# Implementation Plan — Spring Service-Discovery Extractor

Companion to `DESIGN.md` (the decision log) and `CLAUDE.md` (the terse contract).
This document is **task-level and executable**: per build-order step (`DESIGN.md` §16),
it lists concrete tasks, the target Go interfaces, files touched, and a definition of
done. Interface sketches are targets — refine while coding, but don't drift from the
seam shape without updating `DESIGN.md`.

---

## A. Decisions — open questions forced closed

These were "still open" in `CLAUDE.md` / `DESIGN.md`. Locked here so tasks are unambiguous.

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| D1 | Data-flow fallback (REST ladder step 2, opaque returns) | **Defer.** Emit opaque returns (`ResponseEntity<?>`, raw `Object`) as `uncertain` nodes. Leave a no-op `ReturnAnalyzer` seam. | Highest complexity, lowest yield; an uncertain node is honest, not a failure. |
| D2 | Module path | **Rename** `github.com/farhadamjady/super-discovery` → `github.com/farhadamjady/service-discovery`, in step 1 task 0. | Step 1 rewrites every import anyway; cheap now, painful once CI/history reference it. |
| D3 | Spring profiles | **Default + active.** Parse `application.{yml,yaml,properties}` as base; merge `application-<p>.*` for each `<p>` in `spring.profiles.active`; `--profiles` overrides. A value present ONLY in a non-active profile is still resolvable but **caps at `likely`**. | Default-only misses prod topic/URL values; merging all profiles collides dev/prod keys. Active-set is the honest middle. |
| D4 | Chained/nested placeholder depth | **Recursive resolve, depth cap 10, cycle guard.** Support default syntax `${a:fallback}` (use fallback if `a` unresolved → `likely`). Over cap or cycle → leave unresolved → `uncertain`. | Real chains are 1–3 deep; 10 is a safety ceiling, not a target. Determinism needs a hard cap + cycle guard. |
| D5 | Per-pattern confidence mapping | **Table in §B.4** (single source of truth). Reconciles the `CLAUDE.md` §3 vs value-resolver-memory conflict in favor of: **hardcoded literal → `confirmed`; one config/constant indirection → `likely`.** | `CLAUDE.md` line 83 ("resolved placeholder → confirmed") contradicted line 84 ("one indirection → likely"). The resolver memory's rule is the more considered one; adopt it. |
| D6 | Scope creep in current skeleton | **Remove** `DetectOpenAPI`, `DetectDTO` misuse, `dbDetector`, and OpenAPI globs from `FileSpec`. Keep `databases_used` as an empty output field only. | OpenAPI + DB are cut (`DESIGN.md` §15). Skeleton still references them. |
| E1 | Externalized deployment config (Helm/K8s/`.env`) | **In MVP scope** as a placeholder-resolution SOURCE (§8.5). Reverses the earlier "unknowable" stance. | Enterprise Spring services read URLs/topics from Helm/env, not `application.yml`; otherwise coverage tanks to ~50–65% on exactly the services that matter most. |
| E2 | How to resolve Helm | **Static, best-effort trace — never shell out to `helm`/`kustomize`.** Trace `.Values.x` → env-var name → Spring property. | Determinism + no external binary/network dep + honesty. Full render (`_helpers.tpl`, `include`/`tpl`) stays best-effort → unresolved = uncertain. |
| E3 | Which environment's values | **Overlay selection mirrors profiles (D3):** `--environment`/`--values` picks `values-<env>.yaml`; default = base `values.yaml`. Divergent overlays for one key → **one edge per candidate** (`conditional`+`candidate_group`), capped `likely`. | Reuse the established multi-candidate mechanism; don't silently guess prod vs staging. |
| E4 | Env-var ↔ property name mismatch | **Relaxed-binding bridge:** normalize so `payment.service.url` ≡ `PAYMENT_SERVICE_URL` ≡ `payment-service-url` ≡ `paymentServiceUrl`. | Spring relaxed binding is how these actually connect; without it the deploy layer never matches. |
| E5 | Where it lives | **New `KindDeployConfig` + `DeployConfigIndexer`** feeding the same layered `ConfigResolver` (`Index.Config`). Two parse modes: real YAML for rendered K8s/`values.yaml`/`.env`; tolerant text scan for Helm templates (not valid YAML). | Fits the existing Index/resolver seam; no new pipeline phase. |
| E6 | Residual ceiling (still cut) | Spring Cloud Config **Server**, Vault/secret managers, Kustomize patch semantics, full Helm named-template eval, values injected only in CI and never committed → stay `uncertain`. | Runtime/secret sources aren't in the checkout; honest coverage keeps them unresolved, not guessed. |

---

## B. Cross-cutting conventions (apply to every step)

### B.1 Determinism (hard requirement — backend diffs on it)
- **Stable identity keys** (used for sort + dedupe, never emitted raw as IDs):
  - endpoint = `METHOD + " " + composedPath`
  - dependency = `targetName + "|" + detection`
  - kafka edge = `topic + "|" + direction`
  - schema field = `wireName`
- **Marshal phase sorts** every slice by its identity key before encoding; nested
  schema fields sorted by wire name. `encoding/json` with a fixed struct field order.
- No maps iterated into output without sorting keys first. No timestamps, no absolute
  paths, no goroutine-order dependence in output.
- **Golden test asserts byte-stability**: run twice, `bytes.Equal`.

### B.2 Testing strategy
- **Golden-file integration tests** under `testdata/services/<name>/` — a tiny Spring
  service (Java + `application.yml` + optional `.avsc`) with a checked-in
  `expected.json`. Runner scans dir, marshals, compares bytes. Regenerate via
  `go test ./... -update`.
- **Per-detector unit tests**: minimal Java snippet string → parse → run that detector's
  rules → assert emitted edges. One table-driven test file per detector.
- **Resolver unit tests**: expression string → `ValueSet` assertion (Exact/Template/Unknown
  + confidence). Cover concat, ternary, reaching-defs, `${}`, holes, caps.
- Auth/submit: `httptest.Server` for validate + ingest; assert fail-closed on unreachable.

### B.3 Error taxonomy + exit codes (auth is the strictest gate)
| Code | Meaning |
|------|---------|
| 0 | success |
| 1 | generic runtime error (parse/IO) |
| 2 | detection failed (no provider / ambiguous) |
| 10 | auth: missing key |
| 11 | auth: 401 invalid/expired |
| 12 | auth: 403 not entitled |
| 13 | auth: 429 quota exceeded |
| 14 | auth: validation server unreachable (**fail-closed**) |
| 20 | submit failed (re-validation / network) |

### B.4 Confidence mapping (D5 — the single table)
| Pattern | Confidence |
|---------|-----------|
| Hardcoded string literal (URL/topic/path) | `confirmed` |
| Constant/enum resolving to a literal (SymbolIndex) | `confirmed` |
| Concatenation where every segment is Exact | `confirmed` |
| `${x}` resolved via config, one hop | `likely` |
| `${x:default}` using the default | `likely` |
| Chained `${x}`→`${y}` resolved (>1 hop, within cap) | `likely` |
| Value found only in a non-active profile (D3) | `likely` (capped) |
| Resolved via deploy layer (Helm/K8s/`.env`, §8.5) | `likely` (capped) |
| Divergent env overlays → per-candidate edge (E3) | `likely` (capped) |
| Conditional/ternary candidate (one edge per candidate) | `likely` (capped) |
| Template with holes (`http://{?}/users/{id}`) | `uncertain` |
| Unresolved / Unknown / over depth cap / cycle | `uncertain` |
| Repo DTO fully walked (schema) | `confirmed` |
| Schema one indirection (unwrapped custom generic, getter-only, known external) | `likely` |
| Schema dynamic/unresolved (total failure → `{object,uncertain}`) | `uncertain` |

---

## C. Per-step task breakdown

Legend: ☐ task · **→** file · *DoD* = definition of done.

### Step 1 — Provider-seam rewrite (makes it build; replaces stubs)
**Goal:** the seam matches `DESIGN.md` §2/§6/§7 — parser *set* by `FileKind`, grouped
`FileSpec`, `Indexers()`, rule-based `Detector`, shared `Index`, `Protocol` on the model,
deterministic identity. No behavior yet; just the correct shapes, and it compiles.

- ☐ **T1.0** Rename module (D2): `go.mod` + every import path → `.../service-discovery`.
  **→** `go.mod`, all `*.go`.
- ☐ **T1.1** Model: add `Protocol` + `Requiredness` enums; add fields.
  **→** `internal/model/enums.go`, `service.go`, `schema.go`.
  ```go
  // enums.go
  type Protocol string
  const ( ProtoREST Protocol="rest"; ProtoKafka="kafka"; ProtoGRPC="grpc"
          ProtoWebSocket="websocket"; ProtoUnknown="unknown" )

  type Requiredness string
  const ( ReqRequired Requiredness="required"; ReqOptional="optional"; ReqUnknown="unknown" )
  ```
  - `Endpoint`, `Dependency`, `KafkaEdge` gain `Protocol Protocol json:"protocol"`.
  - `Dependency`/`KafkaEdge` gain `Conditional bool json:"conditional,omitempty"` +
    `CandidateGroup string json:"candidate_group,omitempty"` (D-source: value-resolver).
  - `Schema` gains `Required Requiredness json:"required"` (**no** omitempty — backend must
    distinguish `unknown` from `optional`).
  - Remove `DetectOpenAPI`, `DetectDTO` from enums; keep `DetectDTO`? → replace with
    `DetectKafka`/`DetectConfig` usage only. (D6)
- ☐ **T1.2** `FileKind` + grouped `FileSpec`.
  **→** `internal/provider/provider.go`.
  ```go
  type FileKind int
  const ( KindJava FileKind = iota; KindSpringConfig; KindKafkaSchema; KindDeployConfig )

  type FileSpec struct { Groups []FileGroup; Exclude []string }
  type FileGroup struct { Kind FileKind; Include []string }
  ```
- ☐ **T1.3** Parser *set* + Indexer + Index + rule-based Detector.
  **→** `internal/provider/provider.go`.
  ```go
  type Provider interface {
      Name() string
      Match(root string, fs FileTree) (matched bool, score int)
      FileSpec() FileSpec
      Parsers() map[FileKind]Parser          // was Parser() (singular)
      Indexers() []Indexer                   // NEW: build cross-file Index
      Detectors() []Detector                 // now rule-based (below)
  }

  type Parser interface { Parse(path string, src []byte) (ParsedFile, error) }
  type ParsedFile interface{ Path() string; Kind() FileKind }

  type Indexer interface { Name() string; Index(ic *IndexContext, idx *Index) error }

  // Index is the shared cross-file knowledge (DESIGN §7).
  type Index struct {
      Config  ConfigResolver
      Types   TypeIndex
      Symbols SymbolIndex
      Schemas SchemaSources
  }

  // Detector = a set of tree-sitter query rules for one concern + one protocol.
  type Detector interface {
      Name() string
      Protocol() model.Protocol
      Rules() []Rule
  }
  type Rule struct {
      Query   string                 // tree-sitter S-expression over Java
      OnMatch func(mc *MatchContext) // called once per match in the single per-file pass
  }
  type MatchContext struct {
      File     ParsedFile
      Captures map[string]ASTNode    // named captures from the query
      Index    *Index
      Resolver Resolver              // value/target resolver (step 5)
      Out      *model.Service        // detectors append edges here
  }
  ```
  - `IndexContext{ Root string; Files FileTree; Parsed map[string]ParsedFile }`.
  - `ConfigResolver`, `TypeIndex`, `SymbolIndex`, `SchemaSources`, `Resolver`, `ASTNode`
    are interfaces declared here, **implemented in later steps** (empty impls now).
  - Drop `ScanContext.Config` single-resolver shape; `Config`/`ConfigBuilder` in
    `provider.go` replaced by `ConfigResolver` inside `Index`.
- ☐ **T1.4** Deterministic identity helpers.
  **→** `internal/model/identity.go`: `EndpointKey`, `DependencyKey`, `KafkaKey`,
  and `Sort(svc *Service)` that orders every slice + nested fields (B.1).
- ☐ **T1.5** Spring provider conforms to new seam; remove OpenAPI globs + `dbDetector`
  (D6). Grouped `FileSpec`: Java / SpringConfig / KafkaSchema / DeployConfig groups (DeployConfig
  globs in Step 4.5); excludes unchanged.
  Detectors list returns the 5 concern detectors (REST, Feign, RestTemplate, WebClient,
  Kafka), each with empty `Rules()` for now.
  **→** `internal/provider/spring/provider.go`, `detect_*.go`.
- ☐ **T1.6** Create `internal/provider/lang/java` package with `File`/`Node` types
  (fields only; tree-sitter wiring is step 3). Spring parser delegates here.

*DoD:* `go build ./...` and `go vet ./...` pass. `NewService(...)` marshals to valid JSON
with the new fields present. No `main` yet.

---

### Step 2 — Pipeline shell + registry + main (empty JSON end-to-end)
**Goal:** `extractor` runs the full phase sequence and emits valid, empty, byte-stable JSON.
Auth + submit are stubbed but wired into the sequence.

- ☐ **T2.1** `internal/registry`: `Default() []provider.Provider` = `{ spring.New() }`.
  Adding Micronaut later = one line here.
- ☐ **T2.2** `internal/pipeline`: orchestrates the phases from `DESIGN.md` §3.
  ```go
  func Run(ctx context.Context, opt Options) (*model.Service, error)
  // Options{ Root, APIKey, ConfigFile, Profiles []string, SubmitURL, DryRun bool }
  // phases: authGate → detect → collect → parse → index → detect(query) → schema → marshal → submit
  ```
  Each phase a private func; step 2 wires empty implementations (parse returns files,
  index builds empty `Index`, detect runs zero rules, schema no-op).
- ☐ **T2.3** `cmd/extractor/main.go`: flag parsing (`--api-key`, `--config`, `--profiles`,
  `--out`, `--submit-url`, `--dry-run`), key precedence `--api-key > EKG_API_KEY > config`
  (**never logged**; mask in any diagnostic), map errors → exit codes (B.3).
- ☐ **T2.4** Auth-gate stub: `internal/auth.Validate(key) (Entitlement, error)` — step 2
  returns success for any non-empty key, error (code 10) for empty. Real HTTP in step 8.
- ☐ **T2.5** Submit stub: `internal/submit.Submit(url, key, svc)` — no-op unless
  `--submit-url` set; real POST in step 8. `--dry-run` skips submit, writes `--out`.
- ☐ **T2.6** Golden test: run against an empty temp dir with a `@SpringBootApplication`
  stub → assert the exact empty-arrays JSON, twice (byte-stable).

*DoD:* `extractor --api-key x --dry-run --out -` on a trivial Spring dir prints the empty
contract JSON and exits 0; empty key exits 10.

---

### Step 3 — tree-sitter Java parser + QueryEngine
**Goal:** one real tree-sitter query runs against a real `.java` file and dispatches matches.

- ☐ **T3.1** Add dep `github.com/smacker/go-tree-sitter` + Java grammar; confirm cgo builds
  (Go 1.26, Apple clang 17 — `DESIGN.md` §17). Add a build tag / CI note for cgo.
- ☐ **T3.2** `lang/java`: real `Parser.Parse` → `*java.File{ Path, Src, Tree }`;
  `java.Node` wraps `*sitter.Node` with helpers: `Type()`, `Child`, `Text(src)`,
  `NamedChildren`, annotation/argument accessors.
- ☐ **T3.3** `internal/query.Engine`: given a file + all detectors' `Rules()`, compile each
  `Query` once (cache by string), run **one traversal per file**, build `Captures` per
  match, invoke `OnMatch`. A single query must be able to capture class-level +
  method-level annotations together (path composition).
  ```go
  type Engine struct { /* compiled-query cache */ }
  func (e *Engine) Run(f *java.File, rules []provider.Rule, mkCtx func(caps map[string]provider.ASTNode) *provider.MatchContext)
  ```
- ☐ **T3.4** Wire `Engine` into the pipeline's second `detect` phase (replaces T2.2's no-op).
- ☐ **T3.5** Smoke test: a `@RestController` file + a trivial rule capturing the class name
  → assert one match with correct text.

*DoD:* engine runs a query over a real Java file and calls handlers with correct captures;
parallel parse of N files is deterministic (sort files before dispatch).

---

### Step 4 — Config indexer + ConfigResolver (placeholder resolution)
**Goal:** `${...}` resolves per D3/D4 before any edge is emitted.

- ☐ **T4.1** YAML + properties parsers → flatten to dotted keys
  (`payment.service.url`). YAML dep (`gopkg.in/yaml.v3`).
- ☐ **T4.2** Profile merge (D3): base `application.*`, then `application-<p>.*` for active
  set (from `spring.profiles.active` or `--profiles`); override order base→active; track
  per-key origin profile to apply the `likely` cap.
- ☐ **T4.3** `ConfigResolver` impl in `spring`:
  ```go
  // Resolve returns the resolved value, a confidence, and ok.
  Resolve(placeholder string) (value string, conf model.Confidence, ok bool)
  ```
  Recursive `${a}`→`${b}` with **depth cap 10 + cycle guard**; `${a:default}` fallback;
  over-cap/cycle → `ok=false`. Confidence per B.4.
- ☐ **T4.5** `ConfigIndexer` (implements `provider.Indexer`) populates `Index.Config`.
- ☐ **T4.6** Emit `config_dependencies` from resolved/unresolved keys referenced by edges
  (transparency), keyed + sorted.
- ☐ **T4.7** Tests: nested chain, cycle, default syntax, profile override + cap, missing key.

*DoD:* given a yml with a chained placeholder, resolver returns the final literal with the
right confidence; a cycle returns unresolved, not a hang.

---

### Step 4.5 — DeployConfig indexer + relaxed-binding bridge (Helm / K8s / `.env`)
**Goal (E1–E6):** externalized deployment config becomes a second resolution layer, so
`@FeignClient(url="${PAYMENT_SERVICE_URL}")` resolves even when the value lives in a Helm
chart. Extends — does not replace — the Step 4 `ConfigResolver`.

- ☐ **T4.5.1** New `KindDeployConfig` + `FileSpec` group globs: `**/values.yaml`,
  `**/values-*.yaml`/`.yml`, `**/templates/**/*.yaml`, `**/Chart.yaml`, `**/*deployment*.y*ml`,
  `**/*configmap*.y*ml`, `**/.env`, `**/*.env`. `--values`/`--deploy-glob` adds paths.
  **→** `internal/provider/spring/provider.go`, `provider.go` (FileKind).
- ☐ **T4.5.2** Real-YAML sources: parse `values*.yaml`, rendered K8s ConfigMap `data:` +
  Deployment `env:`/`envFrom:`, and `.env` (dotenv) → env-var name → value, with origin
  (which file/overlay). **→** `internal/deployconfig/` (neutral) + spring delegation.
- ☐ **T4.5.3** Helm **template trace** (templates aren't valid YAML): tolerant line/regex scan
  of `env:` entries capturing `name: X` + `value: {{ .Values.a.b.c }}` (and
  `valueFrom.configMapKeyRef`); resolve `.Values.a.b.c` against merged `values.yaml`(+overlay).
  `_helpers.tpl`/`include`/`tpl`/Kustomize → best-effort, unresolved = `uncertain` (E2/E6).
- ☐ **T4.5.4** Relaxed-binding normalizer (E4): `normalizeKey(s)` unifies dotted / `UPPER_SNAKE`
  / kebab / camelCase to one canonical form; used when bridging env-var names ↔ Spring properties.
- ☐ **T4.5.5** Make `ConfigResolver` **layered** + candidate-aware:
  ```go
  type ConfigResolver interface {
      Resolve(placeholder string) (value string, conf model.Confidence, ok bool)
      Candidates(placeholder string) []ResolvedValue // >1 when overlays diverge (E3)
  }
  type ResolvedValue struct {
      Value  string
      Conf   model.Confidence
      Source string // "application.yml" | "values-staging.yaml" | "configmap/foo" | ".env"
      Origin string // profile/overlay name — drives the `likely` cap + provenance
  }
  ```
  Resolution order: Spring `application.*` (active profiles) → DeployConfig env layer →
  `${x:default}` → unresolved. Deploy-layer + non-active-profile hits **cap at `likely`** (B.4).
- ☐ **T4.5.6** `DeployConfigIndexer` (implements `provider.Indexer`) populates the deploy layer;
  overlay selection from `--environment`/`--values`, default base `values.yaml` (E3).
- ☐ **T4.5.7** Divergent overlays → detectors emit **one edge per candidate** via the existing
  `Conditional`+`CandidateGroup` fields (shared with the value resolver, Step 5).
- ☐ **T4.5.8** Bounding/determinism: `.Values` chain depth cap, cycle guard, fixed overlay-merge
  order, deterministic candidate ordering. Topics resolve through this too, not just URLs.
- ☐ **T4.5.9** Tests: Helm `values-staging.yaml` → Deployment template `env:` → Spring
  `${PAYMENT_SERVICE_URL}` → literal (`likely`); staging vs prod divergence → 2 candidate edges;
  relaxed-binding match; ConfigMap env; `.env`; unresolvable Config-Server ref → `uncertain`.

*DoD:* a Feign URL whose value exists only in a Helm chart resolves to the literal at `likely`;
staging/prod overlays that disagree yield two candidate edges tagged with a shared group; a
Config-Server/secret ref stays an unresolved edge (`uncertain`), never dropped.

### Step 5 — Value resolver (`internal/resolve` + Java evaluator)
**Goal:** in-code target recovery (hardcoded / concat / conditional) → `ValueSet`.

- ☐ **T5.1** Neutral `internal/resolve`:
  ```go
  type ValueSet struct { Kind Kind; Values []Value; Template []Segment } // Exact|Template|Unknown
  type Value struct { S string; Conf model.Confidence }
  type Segment struct { Literal string; Hole bool }
  type Resolver interface { Resolve(node ASTNode) ValueSet }
  ```
- ☐ **T5.2** Java evaluator in `lang/java`, consuming `Index` (Symbols + Config):
  literal→Exact; `a+b`/StringBuilder→concat (product, **size-capped**); ternary/switch→
  **UNION**; local var→**reaching definitions** (union of assignments on all control paths);
  constant/enum→SymbolIndex; `@Value("${...}")`→ConfigResolver; known builders
  (`UriComponentsBuilder`, `String.format`)→reconstruct; method param / unknown call /
  `getenv`/`getProperty`→**HOLE**.
- ☐ **T5.3** Bounding: depth cap, candidate-set cap (explosion→degrade to Template/Unknown),
  **no loop analysis** (loop var→hole), memoize per expression, cycle guard, deterministic
  ordering of candidates.
- ☐ **T5.4** Multi-value policy: caller emits **one edge per candidate**, `Conditional=true`
  + shared `CandidateGroup` id; candidate confidence capped `likely` (B.4).
- ☐ **T5.5** `SymbolIndex` indexer (constants/enums → literal values) built here (needed by
  the evaluator).
- ☐ **T5.6** Tests per T5.2 branch + caps.

*DoD:* evaluator turns `"http://" + Const.HOST + "/users/" + id` into the right
Exact/Template with holes and confidences; a 2-branch ternary yields 2 candidates.

---

### Step 6 — Detectors, one at a time
**Goal:** real edges. Order: **REST → Feign → RestTemplate → WebClient → Kafka.** Each is a
rule set + `OnMatch` handlers; each lands with its own unit tests before the next.

- ☐ **T6.1 REST** (`@RestController` + mapping annotations): query captures class-level
  `@RequestMapping` + method-level `@GetMapping/@PostMapping/...` **together**; compose path
  (class + method), keep verb, preserve `{id}`; never emit method path alone. `Protocol=rest`,
  `Detection=annotation`.
- ☐ **T6.2 Feign** (`@FeignClient`): emit raw `name=` as `TargetName` (**not** a service_id);
  resolve `url=${...}` via `Index.Config`; resolved→`URL`+`likely`/`confirmed`, else
  `Resolved=false`+`uncertain`. `Protocol=rest`, `Detection=feign`.
- ☐ **T6.3 RestTemplate** (`getForObject`/`exchange`/`postForEntity`/...): hand the URL-arg
  expression node to `Resolver`; one edge per candidate. `Detection=resttemplate`.
- ☐ **T6.4 WebClient** (`.baseUrl(...)` + `.uri(...)` fluent chain): reconstruct via resolver
  builder support; `Detection=webclient`.
- ☐ **T6.5 Kafka** (`KafkaTemplate.send` producer; `@KafkaListener` consumer): topic via
  resolver+config; **edge always emitted if real**, independent of schema.
  `Protocol=kafka`, `Detection=kafka`; direction by slice.
- ☐ **T6.6 Exclusions**: verify `src/test`, generated, build output already filtered by
  `FileSpec.Exclude`; skip commented-out matches (tree-sitter comment nodes).
- ☐ Golden fixture grows one service covering all five as they land.

*DoD:* each detector: unit test green; the shared golden service reflects the new edges
with correct protocol/detection/confidence, byte-stable.

---

### Step 7 — TypeIndex/SymbolIndex + schema pass
**Goal:** attach request/response + topic schemas via the ladders. Runs after endpoints exist.

- ☐ **T7.1** `TypeIndex` indexer: index repo DTOs — record components, ctor params, declared
  fields, getters, superclass (`extends`), imports, annotations (Jackson/validation/Lombok).
- ☐ **T7.2** `internal/schema` REST **Walker** (`DESIGN.md` §10 steps 1,3–10; **step 2
  deferred per D1** behind a no-op `ReturnAnalyzer`):
  locate type → unwrap containers → resolve name→FQN→TypeDef → **UNION** fields → Jackson
  wire semantics → nullability + requiredness (§11) → merge inherited → recurse nested
  **depth 2** (config knob) + cycle detection → confidence + **always emit**.
- ☐ **T7.3** Requiredness (§11): tri-state; emit always (no omitempty); confidence per B.4.
- ☐ **T7.4** Kafka schema (`internal/schema/kafka.go`, `DESIGN.md` §12): K1 payload+topic
  (unwrap `ConsumerRecord`/`Message`/`List` batch, reuse REST unwrapper) → K2 wire format from
  config serializer → K3 **files-first** (`SchemaSources` .avsc/.proto/JSON-Schema matched by
  Avro name+namespace / Proto message / filename → `confirmed`; else in-code DTO via REST
  Walker → `likely`; **Registry deferred**) → **safe-fail keeps edge, drops schema**.
- ☐ **T7.5** `SchemaSources` indexer + neutral contract parsers in
  `internal/schema/contract/` (Avro/Proto/JSON-Schema); Spring `Parsers()[KindKafkaSchema]`
  delegates. Native nullability/requiredness from files (§12).
- ☐ **T7.6** Wire schema pass into pipeline after detect.

*DoD:* endpoints carry request/response schemas; a topic with a repo `.avsc` gets a
`confirmed` schema; an unresolved payload keeps the edge with `schema:null,uncertain`.

---

### Step 8 — Auth + submit wiring (real network)
**Goal:** replace step-2 stubs with the real fail-closed gate + authenticated submit.

- ☐ **T8.1** `auth.Validate`: `POST /v1/auth/validate` Bearer key → 200 `{plan,
  quota_remaining, expires_at}` proceed; map 401/403/429/unreachable → codes 11/12/13/14.
  **Fail-closed** on unreachable. Runs **before any scan**.
- ☐ **T8.2** `submit.Submit`: authenticated `POST` of the full marshaled graph to the ingest
  API; backend **re-validates** the key (the robust gate). Non-2xx → code 20.
- ☐ **T8.3** Key masking audit: grep that the key never reaches logs/errors/diagnostics.
- ☐ **T8.4** Tests with `httptest`: valid, 401, 429, unreachable (fail-closed), submit
  re-validation reject.

*DoD:* a run with a bad key exits before scanning with the right code; a good key scans and
submits; server down → exit 14, nothing scanned.

---

## D. Sequencing / dependencies
```
1 (seam) ─┬─ 2 (shell) ─── 3 (parser+query) ─┬─ 4 (config) ─── 4.5 (deploy) ─┬─ 5 (resolver) ─── 6 (detectors) ─── 7 (schema)
          │                                    └── (T5.5 SymbolIndex feeds 5) │
          └──────────────────────────────────────────────────────────────────┴─ 8 (auth/submit, independent after 2)
```
- 1 → everything. 2 needs 1. 3 needs 2. 4 needs 3 (queries `@Value`). **4.5 needs 4** (extends the
  same ConfigResolver). 5 needs 4/4.5 (+SymbolIndex). 6 needs 5. 7 needs 6 + TypeIndex.
  **8 can proceed any time after 2** (parallelizable).
- Ship a working `extractor` at the end of **every** step (empty→richer JSON), never a
  half-wired tree.

## E. Risks / watch-items
- **cgo / tree-sitter** (step 3) is the first real integration risk — spike T3.1 early even
  though it's ordered third; if the grammar build fights CI, that reshapes scheduling.
- **Reaching-definitions** (T5.2) is the subtlest code; keep it intra-procedural (MVP scope),
  over-approximate safely, lean on the caps.
- **Byte-stability** regressions are silent until the golden diff — add the twice-run assert
  in step 2 and keep it green thereafter.
- **Requiredness `no-omitempty`** must survive refactors (backend depends on seeing
  `"unknown"`); guard with a golden field-presence test.
