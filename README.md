<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/archerik-banner-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="assets/archerik-banner-light.svg">
  <img alt="Archerik" src="assets/archerik-banner-light.svg" width="400">
</picture>

**Your architecture diagram, generated from the code that actually runs —
every endpoint, dependency, Kafka topic, and schema, on every commit.**

[![CI](https://github.com/farhadamjady/archerik-extractor/actions/workflows/ci.yml/badge.svg)](https://github.com/farhadamjady/archerik-extractor/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26%2B-00B3CB?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-008598)](LICENSE)
[![Stacks](https://img.shields.io/badge/stacks-8-005865)](#supported-stacks)

</div>

---

## What is Archerik?

Archerik extracts engineering knowledge — architecture, contracts, and
dependencies — directly from source code and configuration.

**Archerik Extractor** is the component in this repository: a single Go binary
that reads one service's repository and emits that service's architecture as
JSON.

It is **deterministic static analysis**. It parses code with tree-sitter and
reads configuration files. It never executes or builds the code it scans, and
**the extraction process does not send your source code to an LLM or any other
external service.** This is not an AI code analyzer — the same input always
produces the same output, byte for byte.

## Why it exists

Every engineering org has the same problem: nobody can say with confidence what
talks to what. The wiki diagram was accurate the week it was drawn. Service
catalogs get filled in by hand and rot immediately. The only artifact that
tells the truth is the source code — and reading it by hand across a few hundred
services is not a plan.

So read it mechanically, on every commit, and let the map maintain itself.

## What the extractor does

Point it at a service repository and it produces a machine-readable map of how
that service participates in your system:

- **REST endpoints it exposes** — full composed paths (class-level mapping plus
  method-level mapping), HTTP verbs, path variables preserved.
- **Services it calls** — Feign, RestTemplate, WebClient, `@HttpExchange`,
  Micronaut `@Client`, MicroProfile Rest Client, axios, fetch, `HttpClient`,
  Refit — with the target resolved through the service's real configuration.
- **Kafka topics it produces and consumes** — with the topic name resolved
  rather than left as `${orders.topic}`.
- **The declared schemas** behind each endpoint and message — walked from the
  source DTOs, or from `.avsc` / `.proto` / JSON Schema contract files when
  those exist.
- **Config dependencies** — the keys the service reads, and what they resolved to.

## How extraction works

Five steps, all local:

1. **Detect.** Score the repo against every registered provider and pick exactly
   one. An unrecognized repo fails loudly rather than emitting an empty graph
   that looks like a service with no dependencies.
2. **Parse.** Read the in-scope files with tree-sitter. `src/test`, generated
   code, and commented-out code are excluded — they inflate the graph with edges
   that do not exist.
3. **Index.** Build cross-file facts first: config values, DTO types, constants,
   schema files.
4. **Find edges.** Detectors declare tree-sitter queries; the engine runs them
   all in a single traversal per file and hands back named captures.
5. **Resolve and emit.** Turn placeholders into real values, attach schemas,
   marshal deterministically.

**Step 5 is the hard part.** Finding `@RestController` is easy. The reason a
naive scan produces a graph nobody trusts is that URLs and topic names are
almost never literals. Archerik resolves them through a layered config source:

```
application.yml / .properties  (+ active profiles)
  └→ Helm values*.yaml, traced through chart-template env: blocks
      └→ rendered Kubernetes ConfigMap / Deployment env
          └→ .env files
```

unified by **relaxed binding**, so `payment.service.url` and
`PAYMENT_SERVICE_URL` are the same key. Deployment config is read **statically
as text** — `helm` and `kustomize` are never executed.

Each edge then reports how sure the extractor is:

- **`confirmed`** — the value was a literal in the source.
- **`likely`** — resolved through config indirection.
- **`uncertain`** — computed at runtime, or otherwise not statically knowable.

An edge that cannot be resolved is **still emitted, marked `uncertain`** — never
dropped, never guessed. A graph that quietly omits what it could not figure out
is worse than one that admits it.

## Supported stacks

| Language | Framework |
|---|---|
| Java | Spring Boot |
| Java | Micronaut |
| Java | Quarkus |
| Kotlin | Spring Boot |
| TypeScript | NestJS |
| JavaScript | Express |
| C# | ASP.NET Core |
| Go | `net/http` (no framework) |

Each language has its own tree-sitter parsing layer, and each framework is a
provider on top of it — so adding a framework to a language that already has a
layer is one package and one registry line. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## Install

Requires **Go 1.26+ and a C toolchain** — the tree-sitter grammars are C, so cgo
must be enabled (it is by default; `CGO_ENABLED=0` will not build).

```sh
go install github.com/farhadamjady/archerik-extractor/cmd/extractor@latest
```

Or from a checkout:

```sh
git clone https://github.com/farhadamjady/archerik-extractor
cd archerik-extractor
go build -o extractor ./cmd/extractor
```

The result is a single binary named `extractor`. No JVM, no Node, no runtime
dependencies — and it never builds or installs the dependencies of the code it
scans.

## Usage

```sh
# print the graph to stdout
extractor --root ./my-service

# write it to a file
extractor --root ./my-service --out graph.json

# resolve the staging deploy overlay and the prod Spring profile
extractor --root ./my-service --out graph.json --environment staging --profiles prod
```

In CI, run it on every commit as a full scan of the service. It is deliberately
not incremental: a commit's architectural impact is not confined to its diff — a
config edit ripples into edges in files it never touched, and deletions have to
remove nodes. Because the output is byte-stable, diffing one commit against the
previous one is reliable.

## Configuration

**Nothing is required.** The framework is detected from the repository, and a
standard project layout needs no configuration at all. Everything below is
optional.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | Repository root to scan |
| `--out` | `-` | Output path for the JSON, or `-` for stdout |
| `--repository` | auto | Repo identifier emitted as `repository`; auto-detected from CI env or the git remote |
| `--profiles` | | Comma-separated active Spring profiles |
| `--environment` | | Deploy overlay to resolve, e.g. `staging` |
| `--config-repo` | | Local checkout of an external config repo whose yml/properties feed resolution |
| `--schema-depth` | `2` | Nested-DTO walk depth before truncation |
| `--api-url` | | Archerik API base URL — enables submission |
| `--api-key` | | API key for `--api-url` runs; also `ARCHERIK_API_KEY` |
| `--config` | | Config file holding the API key (`api_key = <value>`) |
| `--dry-run` | `false` | Produce the JSON but do not submit |
| `--branch`, `--sha`, `--pr` | auto | Commit metadata; auto-detected on GitHub Actions |
| `--comment-out` | | Write the API's PR-comment markdown to this file |

If your organization wraps HTTP clients in an internal SDK, a
`.archerik-adapters.json` file at the repository root declares those wrappers —
each entry names a method and which argument carries the target — so their call
sites are detected as real outbound edges. Currently supported on **Spring
(Java)** only.

## Output

One JSON object per service, with these top-level fields:

| Field | Contains |
|---|---|
| `service_id`, `service_name` | Identity of the scanned service |
| `repository` | Repository identifier |
| `language` | Detected language |
| `endpoints` | REST endpoints exposed, with request/response schemas |
| `outbound_dependencies` | Services this one calls |
| `kafka_producers`, `kafka_consumers` | Topics written and read, with message schemas |
| `config_dependencies` | Config keys read, and what they resolved to |
| `databases_used` | Reserved — see limitations |

Every communication edge carries three **orthogonal** fields, so the graph stays
queryable along any of them:

- **`protocol`** — what it is: `rest`, `kafka`, `grpc`, `websocket`, `unknown`.
  Feign, RestTemplate, and WebClient are three *detections* of one *protocol*.
- **`detection`** — how it was found: `feign`, `resttemplate`, `webclient`,
  `axios`, `refit`, `kafka`, `config`, and so on.
- **`confidence`** — `confirmed`, `likely`, or `uncertain`, as above.

Two deliberate omissions:

- **`inbound_dependencies` is never emitted.** A service cannot know who calls
  it. Inbound edges are derived by whatever aggregates the graphs, from everyone
  else's outbound edges.
- **Logical names are emitted raw.** `@FeignClient(name="payment-service")` is a
  logical name, not a service ID. Mapping names to identities is the
  aggregator's job — the extractor does not guess it.

## Accuracy and limitations

Quality here is measured rather than asserted. The extractor is developed
against a labeled benchmark corpus of real open-source repositories — the
petclinic family, `mall`, `piggymetrics`, `bank-of-anthos`, official Spring
Cloud samples — with ground truth pinned to commit SHAs and every miss
adjudicated as either a tool bug or a labelling bug. Scoring is
confidence-aware: an honest `uncertain` on a genuinely runtime value counts as
correct behavior, not a miss. A negative control (a server-rendered view app)
confirms it does not invent endpoints from non-REST methods.

On the most recent round, that benchmark covered **39 services, with 100%
precision and recall on in-scope endpoints and producers.**

**Read that number honestly.** It means that on the categories the extractor
claims to support, in open-source repositories, it neither missed nor invented
edges. It is not a claim about your private codebase. Heavy use of internal SDK
wrappers or a bespoke in-house framework will reduce coverage — that is what
`.archerik-adapters.json` is for on Spring, and what the `uncertain` level is
for everywhere else.

**Not implemented yet:**

- **Database detection** (JPA/JDBC) — `databases_used` is emitted, always empty.
- **gRPC and WebSocket** — the `protocol` enum reserves them; nothing detects
  them yet.
- **Kafka Schema Registry** — schemas come from repo contract files and in-code
  types. Fetching from a live registry needs network access and credentials,
  which would make runs non-reproducible.
- **OpenAPI as a primary source** — read only in the narrow case where the build
  generates controllers from a spec. The code is the source of truth.
- **Full Kubernetes topology** — deployment config is a *placeholder-resolution
  source*, not a workload model.

**Known gaps, documented rather than hidden:**

- Spring Cloud Stream **functional** Kafka consumers (annotation-based ones are
  detected).
- Express: schema extraction for plain-JS validation libraries (Joi,
  express-validator, Mongoose) is not implemented — endpoints still resolve.
- Values that would need **inter-procedural analysis** come out `uncertain`.
- Runtime configuration sources — Spring Cloud Config Server, secret managers —
  are unresolvable by construction, and become `uncertain` edges.
- One provider is selected per repository; a polyglot monorepo should be scanned
  per service.

## Privacy

Extraction runs **entirely in your environment** — your laptop or your CI
runner. With no `--api-url`, the extractor makes no network calls at all: it
reads a directory and writes JSON.

If you do point a run at an Archerik API, **only the derived architecture JSON
is transmitted** — endpoints, dependency names, topics, schema shapes. Source
code is never uploaded, and no code is ever sent to a model. The API key is read
from `--api-key`, `ARCHERIK_API_KEY`, or a config file, and is never logged.

## The Archerik platform

```
Repository → Archerik Extractor → Archerik API → Archerik UI
```

| Component | Repository | Role |
|---|---|---|
| **Extractor** | [`archerik-extractor`](https://github.com/farhadamjady/archerik-extractor) *(this repo)* | Scans one service repository, emits its architecture as JSON. Runs locally or in CI. |
| **API** | [`archerik-api`](https://github.com/farhadamjady/archerik-api) | Ingests graphs, stores them, derives inbound edges across services, diffs each commit against the last. |
| **UI** | [`archerik-ui`](https://github.com/farhadamjady/archerik-ui) | Explores the fleet graph — services, dependencies, topics, schemas — and shows what changed. |

The extractor stands on its own: it prints JSON, which is a complete answer for
a single service. The API and UI turn per-service output into a fleet-wide graph
with history.

`cmd/archerikd` in this repository is a **reference implementation** of the
ingest contract — enough to run the whole loop locally, and a starting point for
building your own backend.

## Contributing

Bug reports about a missed or wrong edge are the most valuable contribution —
please include the source snippet and the JSON it produced. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the architecture, the invariants a change
must respect, and how to add a framework or a language.

## License

[MIT](LICENSE).

<div align="center">
<br>
<img src="assets/archerik-mark.svg" alt="" width="44">
<br><br>
<sub><b>Archerik</b> — architecture intelligence, straight from the source.</sub>
</div>
