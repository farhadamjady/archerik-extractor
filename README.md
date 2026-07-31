# service-discovery — Engineering Knowledge Graph Extractor

A Go CLI that reads a service repository and emits its architecture as JSON:
the REST endpoints it exposes, the services it calls, the Kafka topics it
produces/consumes, and the declared request/response and message schemas
behind them. It's the extraction layer of an **Engineering Knowledge Graph** —
turning source code into static architecture intelligence that a backend can
store, diff across commits, and query.

- **Deterministic** — pure AST + config parsing, no LLM inside the extractor.
- **Byte-stable** — same input always produces the same output, so a backend
  can reliably diff a service's graph commit-to-commit.
- **Paid / gated** — a run validates a per-user API key before scanning and
  submits results to an ingest API. There's no offline free mode.

## Supported stacks

| Language | Frameworks |
|---|---|
| **JVM** (Java + Kotlin) | Spring Boot, Micronaut, Quarkus (Java); Spring Boot (Kotlin) |
| **Node.js** | NestJS, Express |
| **.NET** (C#) | ASP.NET Core (attribute routing + Minimal APIs) |
| **Go** | standard library `net/http` (no framework) |

Each language has its own tree-sitter parsing layer; each framework is a
provider built on top of it. REST endpoints, outbound HTTP clients, and Kafka
producers/consumers are covered per-stack as listed above (Kafka today is
JVM-only). Adding a new framework on an existing language, or a new language
entirely, is a matter of adding one provider package and a registry line —
see [CLAUDE.md](CLAUDE.md) for the full scope and design rules.

## What it extracts

Every communication edge — REST endpoint, outbound call, Kafka producer/consumer —
carries three **orthogonal** fields, so the graph is queryable by any of them:

- **protocol** — `rest | kafka | grpc | websocket | unknown` (the communication semantics)
- **detection** — *how* it was found (`feign`, `resttemplate`, `annotation`, `kafka`, `config`, …)
- **confidence** — `confirmed` (literal value) · `likely` (resolved via config) · `uncertain` (dynamic/unresolvable, still emitted — never silently dropped)

The hard part isn't finding annotations, it's that targets are rarely
hardcoded. URLs and topics usually hide behind config placeholders
(`${payment.service.url}`), so the extractor resolves them through a layered
config source: application config → Helm `values*.yaml` → rendered K8s
manifests → `.env` files, unified by relaxed binding
(`payment.service.url` ≡ `PAYMENT_SERVICE_URL`).

`inbound_dependencies` is intentionally **not** emitted — the backend derives
those from everyone's outbound edges.

## Usage

Requires Go with cgo (tree-sitter grammars are C).

```sh
go build -o extractor ./cmd/extractor

# local/dev: no backend, output to stdout
extractor --root ./my-service --api-key "$EKG_API_KEY" --dry-run

# with a backend: validate the key, scan, and submit the graph
extractor --root ./my-service --api-key "$EKG_API_KEY" --api-url https://api.example.com
```

Key precedence: `--api-key` > `EKG_API_KEY` env > config file. The startup
check is a fail-fast deterrent; the backend re-validates the key at submit
time, which is the real enforcement point.

## Architecture, at a glance

```
cmd/extractor          CLI entrypoint
internal/pipeline      auth-gate → detect → collect → parse → index → detect → schema → marshal → submit
internal/model         the JSON output contract + deterministic identity/sort (Service)
internal/registry      the one place providers are registered
internal/provider/
  lang/<language>      shared parsing layer per language (tree-sitter Node, helpers)
  <framework>/          one package per framework: Match + Parsers + Indexers + Detectors
```

Providers plug into a shared pipeline: a framework is detected by repo
markers, its files are parsed once by its language layer, a shared `Index` is
built (config, symbols, schemas), then detectors run tree-sitter queries
against that index to emit endpoints, dependencies, and schemas.

## Testing

```sh
go test ./...
```

Every provider is benchmarked against real open-source repos (precision/recall
per category), not just unit tests — see `IMPROVEMENTS.md` for the running log
of findings and fixes from that process.

## Scope

**Deferred / cut, by design** (never silent — unresolved cases become
`uncertain` nodes, or are simply not emitted rather than guessed): DB detection
(JPA/JDBC), OpenAPI ingestion, gRPC, full K8s topology, Spring Cloud Config
Server / secret managers, version history.

> Note: config resolution reads `helm`/`kustomize` values *statically* (as flat
> config, for placeholder resolution) — the tool never renders charts/overlays
> or executes templates.

See [CLAUDE.md](CLAUDE.md) for the full spec this project is built against.
