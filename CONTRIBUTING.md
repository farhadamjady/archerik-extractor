# Contributing to Archerik

Thanks for considering a contribution. This document is the working guide for
the codebase — read §1 and §3 before your first change; they are what keep the
output trustworthy.

## Getting set up

You need **Go 1.26+ and a C toolchain** — the tree-sitter grammars are C, so
cgo must be enabled (it is by default; `CGO_ENABLED=0` will not build).

```sh
git clone https://github.com/farhadamjady/archerik-extractor
cd archerik-extractor
go build ./...
go test ./...
```

The gate every change must pass, and what CI runs:

```sh
go build ./... && go vet ./... && gofmt -l . && go test ./...
```

`gofmt -l .` must print **nothing**. Run `gofmt -w .` before committing.

## 1. Architecture in ten lines

- The core never names a framework. `registry.Default()` lists providers;
  detection picks exactly ONE per repo by `Match()` score, and fails loudly on
  a tie or no match rather than emitting a silent empty graph.
- A **Provider** = `Match + FileSpec + Parsers + Indexers + Detectors + NewResolver`.
  The interfaces are all in `internal/provider/provider.go` — that file is the seam.
- The seam is **framework over language**: `provider/lang/java` owns parsing
  (tree-sitter), the value evaluator, the TypeIndex and SymbolIndex.
  `provider/spring` owns only annotation names, config idioms, and detector rules.
- Pipeline (fixed — don't reorder or add phases): auth → detect → collect →
  parse → index → detect(query) → spec-ingest → marshal → submit.
- Detectors **never traverse files**. They declare tree-sitter query `Rule`s; the
  query engine runs every rule in ONE traversal per file and calls your
  `OnMatch(mc)` with named captures.
- Cross-file facts (config values, DTOs, constants, schema files) come from the
  shared `Index`, built by Indexers before detection. Handlers only look up.
- `MatchContext` gives you the captures, the `Index`, the `Resolver` (value
  evaluator), and `Out *model.Service` to append edges to.

Worth reading in order, then stopping: `internal/provider/provider.go`,
`internal/provider/spring/provider.go` (reference wiring),
`internal/provider/spring/detect_rest.go` (a real detector). You will almost
never need to touch the pipeline or the query engine.

## 2. Adding a framework or a language

**A framework on a language that already has a layer** (e.g. another JVM
framework) is small work:

1. Create `internal/provider/<name>/` with a `Provider` struct.
2. `Match`: score the build files plus the framework's entry annotation.
3. `FileSpec` / `Parsers`: reuse the language layer's parser unchanged; keep a
   raw parser for config/schema kinds.
4. `Indexers`: reuse what is framework-neutral. If you need something that
   currently lives in another provider, promote the shared part into a neutral
   package rather than importing that provider.
5. `Detectors`: the real work — map the framework's annotations onto rules.
6. `NewResolver`: return the language layer's evaluator; it is framework-free.
7. Register it: one line in `internal/registry/registry.go`.
8. Tests + a real-repo check (§3).

`internal/provider/micronaut/` and `internal/provider/quarkus/` are worked
examples — both reuse `lang/java` verbatim. Copy their shape.

**A new language** is large work: a new layer mirroring `lang/java` — grammar
wiring, a `File` implementing both `provider.ParsedFile` and
`provider.QueryRunner`, a value evaluator implementing `provider.Resolver`, and
a `schema.TypeSource` if the language has DTO-like types. The `internal/resolve`
lattice and the `internal/schema` walker are language-free; reuse them. Start
with literals + concatenation + constants and add the expensive resolution
steps only when a real repo demands them.

## 3. Invariants — breaking these breaks the product

1. **Determinism / byte-stable output.** Same input → identical bytes. Sort
   everything; never iterate a map into output. Consumers diff on bytes.
2. **Identity keys are law**: endpoint `verb+path`, dependency
   `target|detection`, kafka `topic|direction`. Dedup, diffing, and downstream
   storage all assume them. Changing one means migrating all three.
3. **Honesty.** An edge you cannot resolve is emitted with confidence
   `uncertain` — never silently dropped, never guessed. A known-host template
   with a path hole is `likely` (like a path variable); a *host* hole stays
   `uncertain`. This rule is the product; treat a violation as a bug.
4. **Edge fields are orthogonal**: `protocol` (semantics) vs `detection`
   (provenance) vs `confidence` (certainty). Every new detector sets all three.
   Confidence: literal → `confirmed`; one config/constant hop → `likely`;
   mutable/cross-class/overlay sources → capped at `likely`; unresolvable →
   `uncertain`.
5. **`Schema.Required` has no `omitempty`** — consumers must be able to tell
   `unknown` from `optional`. Every emitted schema sets it.
6. **`IndexContext.Shared` files are types and constants ONLY.** Detectors never
   run on sibling-module files; edges belong to the scanned service.
7. **One tree-sitter pattern per `Rule.Query`.** The engine maps pattern index to
   rule 1:1 and errors if you put two patterns in one rule.
8. Update the `TestDetectors` table in `provider_test.go` when you add a
   detector; `TestParsersCoverFileSpec` guards that kinds and parsers agree.

## 4. Testing

- Every detector test runs through the **real** query engine and parser. Reuse
  the existing helpers rather than writing new plumbing: `scanWith`, `httpDeps`,
  `kafkaScan`, `restEndpoints` (spring), `evalTarget` (evaluator — note it finds
  `target(...)` in the LAST source file, so order your sources), and
  `buildStore` / `buildLayered` for config.
- **Check against a real repository before declaring a detector done.** Unit
  tests passing is not evidence that a detector works: every real-world gap
  found in this codebase so far had green unit tests. Clone an actual service of
  the stack you are adding, run the extractor over it, and hand-verify a
  controller or two against the output.
- When you fix a real-world miss, add the reproducing case as a test in the same
  PR, and re-check the stacks you did *not* touch — a new detector runs on every
  file and can create false positives elsewhere.

## 5. Gotchas that cost real time

**tree-sitter grammars** (dump the AST of a real snippet before assuming
anything — five minutes of printing `Type()` and `Text()` per node beats an hour
of guessing):

- Java annotation array values are `element_value_array_initializer`, not
  `array_initializer`.
- A switch arrow-rule's value is wrapped in an `expression_statement`.
- Annotations live inside the `modifiers` child — including ones placed between
  modifiers and the return type (`public @ResponseBody Vets x()`).
- Parsers are **not concurrency-safe**: use a fresh parser per file.

**Go and tooling:**

- `yaml.v3` decodes numeric map keys (`200:`) as `map[any]any` — stringify keys
  before lookups (see `contract.normalizeKeys`).
- Evaluator memo keys must include the FILE, not just the byte offset —
  evaluation crosses files (creation sites, shared modules).
- Run `gofmt -w` after generating any file; comment alignment will differ.

**Design traps:**

- Detectors are stateless. Cross-file state belongs in an Indexer and the `Index`.
- Don't parse inside a detector. If you need file content beyond the AST (an
  imports gate, say), a cheap `strings.Contains` over `Src()` is acceptable.
- `@Value` can appear on constructor params and fields, and be reached through
  `this.f = param` — the evaluator handles all of it. Don't reimplement it.
- Spring resolves `${}` in annotation strings but **not** in raw code strings.
  Resolution rules are context-dependent; check before assuming.

## 6. Pull requests

- One concern per PR; no unrelated refactors alongside a fix.
- The full gate green, with tests for the behavior you changed.
- If the change affects the JSON output, say so explicitly in the description —
  output shape is a consumer contract.
- Update the README's capability list when you add a stack.

## 7. Scope

Deliberately **out of scope**: LLM inference of any kind inside the extractor,
executing or building the scanned code, rendering Helm/Kustomize (config is read
statically), and sending source code anywhere — only derived JSON leaves the
machine. Deferred for now: database detection, gRPC, OpenAPI ingestion as a
primary source, and full Kubernetes topology.

A PR that improves coverage by guessing will be declined, even if it raises the
numbers. An honest `uncertain` beats a confident wrong edge.
