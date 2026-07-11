# GUIDELINE — for the agent adding new languages / frameworks

Written by the agent that built this codebase (extractor + benchmark + backend).
Follow this and you will spend your tokens writing code, not rediscovering how
things work. Read it fully before touching anything.

---

## 0. Read these files first (in this order, nothing else at the start)

1. `CLAUDE.md` — the product contract (scope, output rules, honesty principle)
2. `internal/provider/provider.go` — THE seam. Every interface you implement is here
3. `internal/provider/spring/provider.go` — the reference implementation's wiring
4. `internal/provider/spring/detect_rest.go` — how a detector really works
5. `IMPROVEMENTS.md` — every real-world gap we found and how it was fixed

Do NOT start by reading the pipeline, the backend, or the evaluator — they
work, they are tested, and you will almost never touch them.

## 1. Architecture in ten lines

- The core never names a framework. `registry.Default()` lists providers;
  detection picks ONE per repo by `Match()` score (fail-loud on tie/none).
- A **Provider** = `Match + FileSpec + Parsers + Indexers + Detectors + NewResolver`.
- The seam is **framework over language**: `provider/lang/java` owns parsing
  (tree-sitter), the value evaluator, TypeIndex, SymbolIndex. `provider/spring`
  owns only annotation names, config idioms, and detector rules.
- Pipeline (fixed, do not change): auth → detect → collect → parse → index →
  detect(query) → spec-ingest → schema → marshal → submit.
- Detectors NEVER traverse files. They declare tree-sitter query `Rule`s; the
  query engine runs ALL rules in ONE traversal per file and calls your
  `OnMatch(mc)` with named captures.
- Cross-file facts (config values, DTOs, constants, schema files) come from the
  shared `Index`, built by Indexers before detection. Handlers only look up.
- `MatchContext` gives you: captures, `Index`, `Resolver` (the value
  evaluator), and `Out *model.Service` to append edges to.

## 2. Recipe A — add a JVM framework (e.g. Micronaut). Cost: small.

1. Create `internal/provider/micronaut/` with a `Provider` struct.
2. `Match`: score build files + the framework's entry annotation
   (`@MicronautApplication`... check the real marker). Copy Spring's shape.
3. `FileSpec` / `Parsers`: copy Spring's; the Java group reuses
   `java.NewParser()` unchanged. Keep `rawParser` for config/schema kinds.
4. `Indexers`: REUSE Spring's logic where it is framework-neutral. The config
   FORMAT may differ (Micronaut also uses application.yml — the yml parser and
   the ${} resolver are reusable as-is; if you need them, promote the shared
   parts out of `spring` into a neutral package instead of importing spring).
5. `Detectors`: this is your real work. Map the framework's annotations:
   Micronaut `@Controller`+`@Get/@Post` ≈ Spring REST detector; `@Client` ≈
   Feign detector. Copy the Spring detector, swap the annotation names, reuse
   `annotations.go`-style helpers (consider hoisting them to `lang/java` —
   they are Java-generic, this was always the plan).
6. `NewResolver`: return `java.NewEvaluator(idx)` — the evaluator is 100%
   framework-free. Zero work.
7. Register: ONE line in `internal/registry/registry.go`.
8. Tests + benchmark (see §5). Done.

## 3. Recipe B — add a language (e.g. Go/Node services). Cost: large.

You must build a new language layer mirroring `lang/java`:
- tree-sitter grammar wiring (`smacker/go-tree-sitter` has many grammars).
  cgo is required and already proven in CI/build.
- A `File` implementing `provider.ParsedFile` AND `provider.QueryRunner`
  (that one interface is what plugs you into the query engine — see
  `java.RunQuery`: combine patterns into one multi-pattern query, dispatch by
  `PatternIndex`, apply `FilterPredicates` for `#eq?`/`#match?`).
- A value evaluator implementing `provider.Resolver` (walk expressions →
  `resolve.ValueSet`). The LATTICE (`internal/resolve`) is language-free:
  reuse `Concat`/`Union`/caps. Start with literals+concat+constants; add the
  fancy steps (reaching defs, call-site union) only when a benchmark repo
  demands them — that is how the Java one grew.
- A `schema.TypeSource` implementation if the language has DTO-like types.
  The schema Walker (`internal/schema`) is language-free — feed it TypeDefs.
- Then a framework provider on top (Recipe A).

## 4. Invariants — break these and you break the product

1. **Determinism / byte-stable output.** Same input → identical bytes. Sort
   everything, never iterate a map into output. The BACKEND DIFFS ON BYTES.
2. **Identity keys are law**: endpoint `verb+path`, dependency
   `target|detection`, kafka `topic|direction`. The diff engine, the dedup,
   and the backend all assume them. Never change without migrating all three.
3. **Honesty**: an edge you cannot resolve is emitted `uncertain` — NEVER
   silently dropped, NEVER guessed. (We shipped a bug that violated this once —
   IMPROVEMENTS #2. Don't repeat it.) Known-host templates with a path hole are
   `resolved/likely` (like path variables); host holes stay `uncertain`.
4. **Edge fields are orthogonal**: `protocol` (semantics) vs `detection`
   (provenance) vs `confidence` (certainty). A new detector sets all three.
   Confidence rules: literal → `confirmed`; one config/constant hop →
   `likely`; mutable/cross-class/overlay sources → capped `likely`;
   unresolvable → `uncertain`.
5. **`Schema.Required` has NO omitempty** — the backend must distinguish
   `unknown` from `optional`. Every schema you emit must set it (see
   `fillRequired`).
6. **`IndexContext.Shared` files are types/constants ONLY** — detectors never
   run on sibling-module files; edges belong to the scanned service.
7. **One tree-sitter pattern per `Rule.Query`** — the engine maps pattern
   index → rule 1:1 and errors if you smuggle two patterns in one rule.
8. Update `TestDetectors` in `provider_test.go` when you add a detector
   (name + protocol table) and `TestParsersCoverFileSpec` guards kinds↔parsers.

## 5. Testing discipline (non-negotiable, and it saves you tokens)

- Every detector test runs through the REAL query engine + REAL parser. Reuse
  the existing helpers instead of writing new plumbing:
  `scanWith` (any detector + full index), `httpDeps`, `kafkaScan`,
  `restEndpoints` (spring pkg); `evalTarget` (evaluator, finds `target(...)`
  in the LAST source file — order your sources!); `buildStore`/`buildLayered`
  (config). Copy their pattern for a new provider.
- Gate: `go build ./... && go vet ./... && gofmt -l . && go test ./...` — all
  clean before every commit. Small commits, one concern each, push to main.
- **Benchmark before declaring victory.** The corpus lives OUTSIDE the repo at
  `../service-discovery-repos/tier-N/<repo>/` with `output.json`,
  `labels.json`, `results.json` and a generic scorer
  `../service-discovery-repos/compare.py <folder>`. For a new framework: clone
  1–2 real repos of that stack, run the extractor, hand-label (or
  count-verify: grep the mapping count + hand-check one controller), score.
  Labels support `"requires": "<missing feature>"` (scored as a gap, not a
  miss) and `_meta.sampled: true` (recall-only, for big repos).
- Every real-world miss becomes a numbered row in `IMPROVEMENTS.md` with the
  repo + commit SHA. Fix it, mark ✅, re-run the benchmark, check for
  regressions across ALL existing tiers (new detectors run on every file —
  they can create false positives in old repos).
- Write your predictions BEFORE running a benchmark. Two of ours were wrong;
  knowing that was valuable.

## 6. Gotchas that cost us real time (read carefully)

**tree-sitter Java grammar shapes** (expect equivalents in other grammars —
always dump the AST of a real snippet before assuming):
- Annotation array values are `element_value_array_initializer`, NOT
  `array_initializer`.
- A switch arrow-rule's value is wrapped in an `expression_statement`.
- Annotations live inside the `modifiers` child — including annotations placed
  between modifiers and the return type (`public @ResponseBody Vets x()`).
- Debug recipe: parse a snippet, `Walk` and print `Type()` + `Text()` per node.
  Five minutes of dumping beats an hour of guessing.

**Go / tooling:**
- `yaml.v3` decodes numeric map keys (`200:`) as `map[any]any` — stringify
  keys before lookups (see `contract.normalizeKeys`).
- macOS `sed` has no `\b` and needs `-i ''`. After ANY bulk python/sed edit,
  grep to verify the replacement actually happened — `str.replace` silently
  no-ops on a mismatch (bit us twice: once from gofmt re-aligning comments).
- Run `gofmt -w` after writing files via heredoc; comment alignment WILL differ.
- Evaluator memo keys must include the FILE, not just the byte offset —
  evaluation crosses files (creation sites, shared modules).
- tree-sitter parsers are not concurrency-safe: fresh parser per file.
- The compiled-query cache is per pattern-set; fine as-is.

**Design traps:**
- Don't add detector state — handlers are stateless; cross-file state goes in
  an Indexer + the `Index`.
- Don't parse anything inside a detector — if you need file content beyond the
  AST (imports gate etc.), `mc.File.(*java.File).Src()` is acceptable for a
  cheap `strings.Contains` gate (see the Kafka Streams / router detectors).
- `@Value` can sit on constructor PARAMS, fields, and be reached via
  `this.f = param` — the evaluator handles all of it; don't reimplement.
- Spring resolves `${}` in annotation strings; raw code strings it does NOT —
  resolution rules are context-dependent (see `resolveTopicNode`).

## 7. File map (where to add things)

```
internal/model/          output contract + identity keys + Sort/dedup — touch ONLY with migration
internal/provider/       the seam interfaces — additive changes only
internal/provider/lang/java/   parser, Node, QueryRunner, evaluator, Types, Symbols — REUSE for JVM
internal/provider/spring/      the reference provider — COPY patterns from here
internal/registry/       one line per provider
internal/resolve/        ValueSet lattice — language-free, reuse
internal/schema/         type model + walker + contract parsers (avsc/proto/json/openapi) — language-free
internal/deployconfig/   Helm/K8s/.env parsing — framework-free, reuse
internal/query/          the engine — done, don't touch
internal/pipeline/       orchestration — rarely touched (Shared modules, spec-ingest hooks live here)
internal/graphdiff/      diff engine + PR-comment renderer (backend feature)
internal/backend/ + cmd/ekgd/   the MVP server (validate/ingest/baselines/name-mapping)
cmd/extractor/           the CLI
```

## 8. Definition of done for a new framework/language

- [ ] Provider registered; detection test (Match scoring) green
- [ ] Detectors with real-engine tests; `TestDetectors` table updated
- [ ] Full gate green (`build/vet/gofmt/test ./...`)
- [ ] 1–2 real benchmark repos cloned, labeled, scored — in-scope ≈100%,
      every miss recorded in `IMPROVEMENTS.md`
- [ ] All EXISTING benchmark tiers re-scored — zero regressions
- [ ] README's capability list updated

One last thing: this codebase got good by the loop *benchmark → finding →
fix → re-benchmark*. Do not skip the loop because unit tests pass — every
single real-world gap we found (23 of them) had green unit tests.
