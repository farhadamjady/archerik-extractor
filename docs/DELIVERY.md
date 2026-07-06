# Delivery Plan — PR-by-PR review sequence

The review layer over `PLAN.md`. `PLAN.md` says *what each build step contains*; this doc
slices that into **small, single-concern, independently-reviewable PRs**. Follow the numbers
in order. Each PR references the PLAN task IDs it implements — the detail lives there.

## Rules every PR obeys
1. **Builds green:** `go build ./...` + `go vet ./...` pass.
2. **Tested:** its own unit/golden tests ship in the same PR and pass.
3. **One concern:** no unrelated refactors; a PR touches only its slice.
4. **Stays runnable:** from PR 4 on, `extractor` runs end-to-end (richer output each time).
5. **Byte-stable:** any PR that touches output keeps the twice-run golden assert green.
6. **Sized to review:** S ≤ ~150 LOC, M ≤ ~350 LOC, L flagged explicitly with why.

## Review milestones (where the tool is demoably better)
- **After PR 4** — runs end-to-end, emits valid empty-contract JSON.
- **After PR 7** — real REST endpoints in the output.
- **After PR 11** — config + Helm/K8s/`.env` placeholder resolution working.
- **After PR 18** — all edges (REST · Feign · RestTemplate · WebClient · Kafka), resolved.
- **After PR 22** — request/response + topic schemas attached.
- **After PR 24** — shippable: auth gate + authenticated submit.

## Master sequence
| PR | Build step | Title | Depends | Size | Reviewer focuses on |
|----|-----------|-------|---------|------|---------------------|
| 1 | 1 | Module rename + model additions | — | S | Field/enum names, JSON tags, `no-omitempty` on `Required` |
| 2 | 1 | Provider seam + Spring conformance | 1 | M | Interface shapes (the seam contract) |
| 3 | 2 | Registry + pipeline skeleton | 2 | M | Phase ordering, `Options`, error→exit-code mapping |
| 4 | 2 | `cmd/extractor` CLI + auth/submit stubs | 3 | M | Key precedence & masking, exit codes, **empty-JSON golden** |
| 5 | 3 | tree-sitter Java parser (cgo) | 4 | M | The cgo dep + `java.Node` accessor API — **de-risk early** |
| 6 | 3 | QueryEngine + wire into detect phase | 5 | M | One-pass traversal, capture dispatch, determinism |
| 7 | 6 (REST) | REST endpoint detector | 6 | M | Path composition (class+method), verb, `{id}` preserved |
| 8 | 4 | Config parse + profiles (ConfigIndexer) | 4 | M | Profile merge order (D3), key flattening |
| 9 | 4 | Placeholder resolver | 8 | M | Chained/cycle/`${x:def}`, depth cap 10, confidence (B.4) |
| 10 | 4.5 | Deploy config: relaxed-binding + YAML sources | 9 | M | `normalizeKey` (E4), ConfigMap/Deployment env/`.env` parse |
| 11 | 4.5 | Deploy config: Helm trace + layered resolver | 10 | L | `.Values` scan (E2), overlay candidates (E3), layer merge |
| 12 | 6 (Feign) | Feign detector | 9 | S | Raw `name` as target (not service_id), `url` via config |
| 13 | 5 | ValueSet + `internal/resolve` + SymbolIndex | 6 | M | Lattice shape (Exact/Template/Unknown), constant indexing |
| 14 | 5 | Java evaluator — literal/concat/const/builder | 13 | M | Concat product cap, builder reconstruction, holes |
| 15 | 5 | Java evaluator — ternary/reaching-defs + bounds | 14 | L | Reaching-defs union (the subtle bit), no-loop, caps |
| 16 | 6 (RestTemplate) | RestTemplate detector | 15 | M | URL-arg → resolver → one edge per candidate |
| 17 | 6 (WebClient) | WebClient detector | 15 | M | `.baseUrl().uri()` fluent-chain reconstruction |
| 18 | 6 (Kafka) | Kafka producer + consumer detector | 15, 11 | M | Edge-always-emitted, topic resolution, batch unwrap |
| 19 | 7 | TypeIndex indexer | 6 | M | DTO fields/getters/ctor/superclass/annotations captured |
| 20 | 7 | REST schema Walker — core | 19 | L | Unwrap → resolve → **field UNION** → Jackson wire names |
| 21 | 7 | Schema nullability/requiredness/inherited/nested | 20 | M | Tri-state `required` (§11), depth-2 + cycle, always-emit |
| 22 | 7 | Kafka schema files-first + contract parsers | 20, 18 | L | Avro/Proto/JSON-Schema parse, safe-fail keeps edge |
| 23 | 8 | Auth validate (real, fail-closed) | 4 | M | Fail-closed on unreachable, exit codes 10–14 |
| 24 | 8 | Submit (real) + key-masking audit | 23 | M | Re-validate at submit, key never logged (code 20) |

Parallelizable once their deps land: **23/24 (auth) after PR 4**; **19 (TypeIndex) after PR 6**;
**13 (ValueSet) after PR 6**. Everything else is the critical path.

---

## Per-PR detail

### PR 1 — Module rename + model additions  · PLAN T1.0, T1.1, T1.4
- **Files:** `go.mod` + all imports; `internal/model/{enums,service,schema,identity}.go`.
- **Adds:** `Protocol`, `Requiredness` enums; `Protocol` on Endpoint/Dependency/KafkaEdge;
  `Conditional`+`CandidateGroup` on Dependency/KafkaEdge; `Required` on Schema (**no omitempty**);
  identity keys + `Sort(svc)`. Removes `DetectOpenAPI`/`DetectDTO` misuse (D6).
- **Tests:** marshal `NewService(...)` → golden; `Sort` idempotence.
- **Accept:** model marshals with all new fields; `go build ./...` green. No behavior yet.

### PR 2 — Provider seam + Spring conformance  · PLAN T1.2, T1.3, T1.5, T1.6
- **Files:** `internal/provider/provider.go`; `internal/provider/spring/*`;
  `internal/provider/lang/java/{file,node}.go` (types only).
- **Adds:** `FileKind` (+`KindDeployConfig`), grouped `FileSpec`, `Parsers()` set, `Indexer`,
  `Index`, rule-based `Detector`+`Rule`+`MatchContext`, `ConfigResolver`/`TypeIndex`/… interfaces
  (empty impls). Spring provider conforms; detectors return empty `Rules()`; OpenAPI/DB removed.
- **Note:** must include Spring conformance in the same PR or the tree won't build (inherent to
  an interface change). Mechanical to review — focus on the seam shapes, not the stubs.
- **Tests:** compile-only + a table test that `spring.New()` satisfies `provider.Provider`.
- **Accept:** `go build ./...` green; seam matches DESIGN §2/§6/§7.

### PR 3 — Registry + pipeline skeleton  · PLAN T2.1, T2.2
- **Files:** `internal/registry/registry.go`; `internal/pipeline/pipeline.go`.
- **Adds:** `registry.Default()`; `pipeline.Run(ctx, Options) (*model.Service, error)` wiring the
  phase sequence with empty phase bodies (collect/parse real via `scan.osFileTree`; index/detect/
  schema no-op). `Options{Root, APIKey, ConfigFile, Profiles, Environment, SubmitURL, DryRun}`.
- **Tests:** `Run` over a temp dir returns an empty, sorted `Service`.
- **Accept:** in-memory empty graph produced through all phases.

### PR 4 — CLI + auth/submit stubs  · PLAN T2.3–T2.6  ·  **milestone: empty JSON**
- **Files:** `cmd/extractor/main.go`; `internal/auth/auth.go` (stub); `internal/submit/submit.go` (stub).
- **Adds:** flags, key precedence `--api-key > EKG_API_KEY > config` (masked, never logged),
  error→exit-code table (B.3), `--dry-run`/`--out`. Auth stub accepts any non-empty key.
- **Tests:** golden empty-contract JSON (run twice, byte-equal); empty key → exit 10.
- **Accept:** `extractor --api-key x --dry-run --out -` prints the contract and exits 0.

### PR 5 — tree-sitter Java parser  · PLAN T3.1, T3.2  ·  **cgo risk — do early**
- **Files:** `go.mod` (smacker/go-tree-sitter + Java grammar); `lang/java/{parser,node}.go`.
- **Adds:** real `Parse` → `*java.File{Path,Src,Tree}`; `java.Node` accessors (Type/Child/Text/…).
- **Tests:** parse a `@RestController` file, assert node types + text extraction.
- **Accept:** cgo builds on the target toolchain (DESIGN §17); parser round-trips a real file.

### PR 6 — QueryEngine + wire  · PLAN T3.3–T3.5
- **Files:** `internal/query/engine.go`; pipeline detect phase.
- **Adds:** compile-query cache; **one traversal per file** building `Captures`, invoking `OnMatch`;
  wired into the second detect phase (files sorted → deterministic dispatch).
- **Tests:** trivial rule captures a class name → one match, correct text.
- **Accept:** engine dispatches captures to handlers; empty JSON still emitted (no detectors yet).

### PR 7 — REST endpoint detector  · PLAN T6.1  ·  **milestone: real endpoints**
- **Files:** `spring/detect_rest.go` + rules.
- **Adds:** query capturing class `@RequestMapping` + method `@GetMapping/...` together; path
  composition, verb kept, `{id}` preserved; `Protocol=rest`, `Detection=annotation`. No config needed.
- **Tests:** unit (snippet → endpoints); grow shared golden fixture `testdata/services/petstore/`.
- **Accept:** endpoints appear with composed paths; byte-stable.

### PR 8 — Config parse + profiles  · PLAN T4.1, T4.2, T4.5
- **Files:** `internal/config/*` (or `spring/config.go`); `ConfigIndexer`.
- **Adds:** YAML+properties parse → flattened dotted keys; profile merge (base + active, D3) with
  per-key origin tracking. No `${...}` resolution yet.
- **Tests:** flatten + profile override + origin tag.
- **Accept:** `Index.Config` holds merged keys; active-profile override wins.

### PR 9 — Placeholder resolver  · PLAN T4.3, T4.6, T4.7
- **Files:** config resolver impl; `config_dependencies` emission.
- **Adds:** recursive `${a}`→`${b}` (cap 10 + cycle guard), `${a:default}`, confidence per B.4;
  emit sorted `config_dependencies`.
- **Tests:** nested chain, cycle (no hang), default syntax, missing key, non-active-profile cap.
- **Accept:** a chained placeholder resolves to its literal with the right confidence.

### PR 10 — Deploy config: relaxed-binding + YAML sources  · PLAN T4.5.1, T4.5.2, T4.5.4
- **Files:** `internal/deployconfig/*`; `KindDeployConfig` globs in Spring `FileSpec`.
- **Adds:** `normalizeKey` (dotted/UPPER_SNAKE/kebab/camel → canonical); parse `values*.yaml`,
  rendered K8s ConfigMap `data:` + Deployment `env:`/`envFrom:`, `.env` → env layer with origin.
- **Tests:** relaxed-binding equivalences; ConfigMap env; `.env`.
- **Accept:** deploy env values indexed under canonical keys.

### PR 11 — Deploy config: Helm trace + layered resolver  · PLAN T4.5.3, T4.5.5–T4.5.9  ·  L
- **Files:** deployconfig Helm scanner; layered `ConfigResolver` merge; `DeployConfigIndexer`.
- **Adds:** tolerant `.Values` scan of template `env:` blocks (E2); layered resolution order
  (Spring active → deploy → default); overlay selection `--environment`; divergent overlays →
  candidates; deploy-layer cap `likely`; `.Values` depth cap + cycle guard.
- **Why L:** two parse modes + merge + candidates. If it grows past ~350 LOC, split the Helm
  scanner from the resolver merge.
- **Tests:** Helm `values-staging.yaml` → Deployment template → Spring `${VAR}` → literal (`likely`);
  staging≠prod → 2 candidates; Config-Server ref → uncertain (kept).
- **Accept:** a Feign URL living only in a Helm chart resolves; divergence yields candidate edges.

### PR 12 — Feign detector  · PLAN T6.2
- **Files:** `spring/detect_feign.go` + rules.
- **Adds:** raw `name=` → `TargetName` (not service_id); `url=${...}` via `Index.Config`; resolved →
  URL+`likely`/`confirmed`, else `Resolved=false`+`uncertain`. `Protocol=rest`, `Detection=feign`.
- **Tests:** name-only; url placeholder resolved (incl. via Helm layer); unresolved.
- **Accept:** Feign edges emitted with correct provenance/confidence.

### PR 13 — ValueSet + resolve pkg + SymbolIndex  · PLAN T5.1, T5.5
- **Files:** `internal/resolve/valueset.go`; `SymbolIndex` indexer.
- **Adds:** `ValueSet{Exact|Template|Unknown}`, `Value{S,Conf}`, `Segment`, `Resolver` iface;
  constant/enum → literal indexing.
- **Tests:** ValueSet constructors; SymbolIndex resolves `Const.X → "x"`.
- **Accept:** lattice + symbol lookups ready for the evaluator.

### PR 14 — Java evaluator: literal/concat/const/builder  · PLAN T5.2 (part), T5.3
- **Files:** `lang/java/evaluator.go`.
- **Adds:** literal→Exact; `a+b`/StringBuilder→concat (size-capped); constant/enum→SymbolIndex;
  `@Value("${...}")`→ConfigResolver; `UriComponentsBuilder`/`String.format`→reconstruct;
  param/unknown/getenv→HOLE. Memoize + deterministic order.
- **Tests:** each branch; concat product cap; hole cases.
- **Accept:** `"http://"+Const.HOST+"/u/"+id` → correct Exact/Template + confidences.

### PR 15 — Java evaluator: ternary/reaching-defs + bounds  · PLAN T5.2 (rest), T5.4  ·  L
- **Files:** `lang/java/evaluator.go` (cont.).
- **Adds:** ternary/switch→UNION; local var→reaching definitions (union over control paths);
  candidate-set cap (explosion→Template/Unknown); no-loop (loop var→hole); cycle guard.
  Multi-value policy hook = one edge per candidate (`Conditional`+`CandidateGroup`).
- **Why L:** reaching-defs is the subtlest logic — isolate it so review can concentrate here.
- **Tests:** 2-branch ternary → 2 candidates; var reassigned on branches → union; loop → hole; cap.
- **Accept:** conditional targets become capped candidate sets, deterministic.

### PR 16 — RestTemplate detector  · PLAN T6.3
- **Adds:** `getForObject`/`exchange`/`postForEntity`/… → URL-arg expr → `Resolver` → one edge per
  candidate. `Detection=resttemplate`.
- **Tests:** literal URL; concat; conditional (candidates). **Accept:** edges with resolved/holed URLs.

### PR 17 — WebClient detector  · PLAN T6.4
- **Adds:** `.baseUrl(...)`+`.uri(...)` fluent chain reconstruction via resolver. `Detection=webclient`.
- **Tests:** baseUrl+uri compose; templated uri. **Accept:** WebClient edges resolved.

### PR 18 — Kafka producer + consumer detector  · PLAN T6.5, T6.6
- **Adds:** `KafkaTemplate.send` (producer) + `@KafkaListener` (consumer); topic via resolver+config
  (incl. deploy layer); **edge always emitted if real**, independent of schema; direction by slice;
  batch/ConsumerRecord unwrap for the payload type (schema in PR 22). Exclusions verified.
- **Tests:** produce+consume; topic via `${...}`; topic via constant. **Accept:** kafka edges, `Protocol=kafka`.

### PR 19 — TypeIndex indexer  · PLAN T7.1
- **Adds:** index repo DTOs — record components, ctor params, declared fields, getters, `extends`,
  imports, Jackson/validation/Lombok annotations.
- **Tests:** record; POJO+getters; Lombok `@Data`; inheritance. **Accept:** `Index.Types` queryable.

### PR 20 — REST schema Walker core  · PLAN T7.2 (steps 1,3–6,10)  ·  L
- **Adds:** locate type (req=`@RequestBody`, resp=return); unwrap containers; resolve name→FQN→
  TypeDef; **field UNION** (records/ctor/fields/getters/Lombok, dedupe by wire name); Jackson wire
  semantics; confidence + always-emit. **Step 2 (data-flow) deferred behind no-op hook (D1).**
- **Why L:** the union + unwrap is the heart of dirty-tolerance. Split unwrap from union if >350 LOC.
- **Tests:** `List<User>`; `Optional<T>`; `Page<T>`; `@JsonProperty` rename; unresolved→uncertain.
- **Accept:** endpoints carry request/response schemas.

### PR 21 — Nullability/requiredness/inherited/nested  · PLAN T7.2 (steps 7–9), T7.3
- **Adds:** nullability rules; tri-state `required` (§11, emit always); inherited-field merge
  (walk `extends`, stop at Object/external); nested recurse depth-2 (knob) + cycle → `{object,truncated}`.
- **Tests:** required vs optional vs unknown; inherited fields; depth-2 truncation; cycle.
- **Accept:** schema fields carry honest tri-state requiredness + bounded nesting.

### PR 22 — Kafka schema files-first + contract parsers  · PLAN T7.4, T7.5, T7.6  ·  L
- **Files:** `internal/schema/contract/{avro,proto,jsonschema}.go`; `SchemaSources` indexer;
  `internal/schema/kafka.go`.
- **Adds:** K2 wire format from serializer config; K3 files-first (match by Avro name+namespace /
  Proto message / filename → confirmed; in-code DTO via Walker → likely; Registry deferred);
  **safe-fail keeps edge, drops schema**; native nullability/requiredness from files.
- **Tests:** `.avsc` matched → confirmed; in-code fallback → likely; unresolved → `schema:null,uncertain`.
- **Accept:** topics get schemas or a kept-but-unschematized edge.

### PR 23 — Auth validate (real)  · PLAN T8.1, T8.4
- **Adds:** `POST /v1/auth/validate` Bearer; 200→entitlement→proceed; 401/403/429/unreachable→
  exit 11/12/13/14; **fail-closed**; runs before any scan.
- **Tests:** `httptest` for each outcome incl. unreachable. **Accept:** bad key exits before scanning.

### PR 24 — Submit (real) + masking audit  · PLAN T8.2, T8.3, T8.4
- **Adds:** authenticated POST of the full marshaled graph; backend re-validates (code 20 on reject);
  audit that the key never reaches logs/errors/diagnostics.
- **Tests:** `httptest` submit accept + reject; grep-style masking test. **Accept:** full run submits;
  server down → exit 14, nothing scanned.

---

## How to run a review cycle
1. I open PR *n* against the previous; diff is one concern + its tests.
2. You review the **Reviewer-focus** column for that PR (the load-bearing bit).
3. CI = `go build ./... && go vet ./... && go test ./...`; goldens updated with `-update` are
   part of the diff, so behavior changes are visible as JSON diffs.
4. Merge → next PR. Never stack a PR on an unmerged one except the flagged parallel tracks.
