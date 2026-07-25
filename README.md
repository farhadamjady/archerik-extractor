# service-discovery — Engineering Knowledge Graph Extractor

A Go CLI that reads **one Spring Boot (Java) service repository** and emits its
architecture as JSON: the REST endpoints it exposes, the services it calls
(Feign / RestTemplate / WebClient), the Kafka topics it produces and consumes,
and the declared request/response and message schemas behind them.

It is **deterministic** — pure AST + config parsing, no LLM inside the
extractor — and produces **byte-stable** output so a backend can diff a service's
graph across commits. It is **paid/gated**: a run validates a per-user API key
before scanning and submits results to an ingest API.

> Part of an Engineering Knowledge Graph MVP: static architecture intelligence
> from source code. Scope started narrow — **Spring Boot + Kafka** — and now also
> covers **Micronaut** and **Quarkus** (Java), and **Spring Boot (Kotlin)** and **NestJS (Node.js/TypeScript)** — spanning JVM and Node stacks on per-language tree-sitter layers.

---

## What it extracts

| Output | From | Notes |
|---|---|---|
| **REST endpoints** | `@RestController` + `@GetMapping`/`@PostMapping`/… | Full path = class `@RequestMapping` + method mapping; verb + path variables preserved; `@RequestBody`/return-type schemas attached |
| **Outbound HTTP deps** | `@FeignClient`, `RestTemplate`, `WebClient` | Feign emits the raw logical name (not a service id); URLs resolved through config/Helm; three detections of one protocol (`rest`) |
| **Kafka producers/consumers** | `KafkaTemplate.send`, `@KafkaListener` | Edge always emitted if real; topic resolved through the same config stack; payload schema attached files-first |
| **Schemas** | Java DTOs, `.avsc` / `.proto` / JSON-Schema | Field union + Jackson wire names, nullability, tri-state requiredness, inheritance, depth-2 nesting |
| **Config dependencies** | `application.*`, Helm/K8s/`.env` | The config keys a service depends on, resolved or not |

Every communication edge carries three **orthogonal** fields:

- `protocol` — `rest | kafka | grpc | websocket | unknown` (semantics; first-class)
- `detection` — `feign | resttemplate | webclient | kafka | annotation | config` (how it was found)
- `confidence` — `confirmed | likely | uncertain` (how sure we are)

### Example output

For a controller that exposes `GET /orders/{id}`, calls `payment-service` via
Feign (URL from a Helm chart), and produces to a Kafka topic backed by an Avro
schema:

```json
{
  "service_id": "orders",
  "service_name": "orders",
  "repository": "",
  "endpoints": [
    { "method": "GET", "path": "/orders/{id}",
      "response": { "type": "Order", "required": "unknown",
        "nested": [
          { "name": "id", "type": "string", "required": "required", "confidence": "confirmed" },
          { "name": "total", "type": "number", "nullable": true, "required": "optional", "confidence": "confirmed" }
        ], "confidence": "confirmed" },
      "protocol": "rest", "detection": "annotation", "confidence": "confirmed" }
  ],
  "outbound_dependencies": [
    { "target_name": "payment-service", "url": "http://payment.prod:8080",
      "protocol": "rest", "detection": "feign", "confidence": "likely", "resolved": true }
  ],
  "kafka_producers": [
    { "topic": "orders.v1", "resolved": true,
      "schema": { "type": "OrderEvent", "required": "unknown", "nested": [ /* ... */ ], "confidence": "confirmed" },
      "protocol": "kafka", "detection": "kafka", "confidence": "confirmed" }
  ],
  "kafka_consumers": [],
  "databases_used": [],
  "config_dependencies": [ /* ... */ ]
}
```

`inbound_dependencies` is intentionally **not** emitted — the backend derives
those from everyone's outbound edges. Unresolved edges are still emitted, marked
unknown/uncertain. Emit raw; the backend stores and maps.

---

## The hard part: resolving targets

Finding annotations is easy. The challenge is that **URLs and topics are almost
never hardcoded** — they hide behind config placeholders, constants, and
externalized deployment config. The extractor resolves them through two
complementary mechanisms.

### 1. Layered config resolution

A placeholder like `@FeignClient(url="${payment.url}")` is resolved through a
layered `ConfigResolver`, in order:

1. **Spring config** — `application.yml`/`.properties` merged across the base +
   active profiles (`spring.profiles.active` or `--profiles`), with recursive
   `${a}→${b}` expansion (depth-capped, cycle-guarded) and `${x:default}` support.
2. **Externalized deployment config** (the enterprise reality) — **Helm**
   `values*.yaml` traced through chart-template `env:` blocks
   (`{{ .Values.x }}` → env-var name → property), rendered **K8s** ConfigMap /
   Deployment env, and **`.env`** files. Unified by **relaxed binding**
   (`payment.service.url` ≡ `PAYMENT_SERVICE_URL` ≡ `paymentServiceUrl`).

A value found only in the deploy layer caps at `likely`; when overlays diverge
(staging vs prod) the extractor emits **one edge per candidate**. Truly runtime
sources (Spring Cloud Config Server, secret managers) stay `uncertain` — honest,
not guessed.

### 2. In-code value evaluator

For targets built in code (`RestTemplate`, `WebClient`, Kafka topics), a
bounded, intra-procedural evaluator walks the argument expression and returns a
`ValueSet` lattice (`Exact | Template | Unknown`):

- literals, string concatenation, `String.format`, known builders;
- constants and enums (via a cross-file `SymbolIndex`);
- `@Value`-injected fields (through the config resolver above);
- **conditionals** (ternary/switch → union) and **local variables** (reaching
  definitions — the union of assignments that reach the use, loops excluded);
- unknown parts become **holes**, so a partially-known target keeps its shape:
  `"http://" + host + "/users/" + id` → `http://{?}/users/{id}`.

Multi-valued results become one edge per candidate. Everything is capped and
cycle-guarded for determinism.

---

## Architecture

The core never names a framework. Everything stack-specific sits behind a
**framework-over-language seam**.

```
cmd/extractor           the CLI (flags, key precedence, exit codes)
  └─ internal/pipeline  orchestrates the phases (below); the only place a Service is marshaled

Phases:  auth-gate → detect → collect → parse → index → detect(query) → schema → marshal → submit

internal/
  model        the JSON output contract + deterministic identity/sort
  registry     the one place providers are registered (add Micronaut = one line)
  detect       picks the single provider from markers; fails loud on none/ambiguous
  scan         filesystem-backed FileTree (glob + excludes)
  query        language-agnostic query engine: one tree-sitter traversal per file
  resolve      the neutral ValueSet lattice + algebra
  deployconfig Helm/K8s/.env parsing + relaxed-binding normalizer
  schema       neutral type model + the schema Walker; contract/ = Avro/Proto/JSON-Schema parsers
  auth,submit  the paid gate (validate + ingest); exitcode = the code taxonomy

  provider/            the seam: Provider = Match + FileSpec + Parsers + Indexers + Detectors
    lang/java          shared Java layer: tree-sitter parser, Node, value evaluator, TypeIndex, SymbolIndex
    spring             the Spring provider: markers, config idioms, and the 5 detectors
```

**Key design choices**

- **Detectors declare tree-sitter query rules**; one traversal per file dispatches
  matches to handlers. Handlers compose paths / resolve targets in Go (clearer
  than doing it in query S-expressions).
- **A shared `Index`** (`Config`, `Types`, `Symbols`, `Schemas`) is built once,
  before detection; handlers look values up rather than re-parsing.
- **Neutral shared logic** (`resolve`, `deployconfig`, `schema`) lives outside the
  Spring package on purpose, so a second Java framework (Micronaut) reuses
  `lang/java` and these packages verbatim — new provider package + one registry line.
- **Determinism** is a hard requirement: stable node/edge identity, sorted output,
  byte-stable marshaling — so the backend can diff commits reliably.

---

## Usage

Requires **Go 1.26+** with **cgo** (the tree-sitter Java grammar is C). Build:

```sh
go build -o extractor ./cmd/extractor
```

Run against a service repo:

```sh
# local/dev: no backend configured, output to stdout
extractor --root ./my-service --api-key "$EKG_API_KEY" --dry-run

# with a backend: validate the key, scan, and submit the graph
extractor --root ./my-service --api-key "$EKG_API_KEY" --api-url https://api.example.com
```

### Flags

| Flag | Meaning |
|---|---|
| `--root` | Repository root to scan (default `.`) |
| `--api-key` | API key. Precedence: `--api-key` > `EKG_API_KEY` env > config file. Never logged. |
| `--config` | Config file path (currently used for the API-key fallback) |
| `--profiles` | Comma-separated active Spring profiles (overrides `spring.profiles.active`) |
| `--environment` | Deploy overlay to resolve, e.g. `staging` (selects `values-<env>.yaml`) |
| `--api-url` | Backend base URL for key validation + submit; empty runs local/dev |
| `--out` | Output path for the JSON, or `-` for stdout (default `-`) |
| `--dry-run` | Produce JSON but do not submit |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | generic runtime error (parse / IO) |
| 2 | detection failed — no provider matched, or ambiguous |
| 10 | auth: missing key |
| 11 / 12 / 13 | auth: invalid-or-expired / not-entitled / quota-exceeded |
| 14 | auth: validation server unreachable (**fail-closed**) |
| 20 | submit failed (backend re-validation / network) |

The **startup key check is a soft, fail-fast deterrent**; the robust enforcement
is the backend **re-validating the key at submit time** (a local binary's check
is bypassable). When no `--api-url` is set, the startup gate is presence-only.

---

## Scope (MVP)

**In:** Spring Boot (Java) + Kafka · REST endpoints · Feign/RestTemplate/WebClient
· Kafka producers/consumers · layered config + Helm/K8s/`.env` resolution ·
in-code value resolution · code-derived schemas (Java DTOs) · Kafka contract
files (Avro/Proto/JSON-Schema) · the paid gate.
**Also in: Micronaut (Java)** — `@Controller`+`@Get/@Post/...` REST endpoints
(including the API-interface pattern where mappings live on an implemented
interface in a sibling module), `@Client` declarative HTTP clients, and
`@KafkaClient`/`@KafkaListener` producers/consumers. Reuses the shared Java
language layer; config-placeholder resolution for Micronaut is pending.
**Also in: Quarkus (Java)** — JAX-RS REST endpoints (`@Path` + `@GET`/`@POST`/…,
verb and path as separate annotations, incl. the API-interface pattern) and
`@RegisterRestClient` MicroProfile REST clients. Reactive-messaging
(`@Incoming`/`@Outgoing`/`@Channel`) and OpenAPI-generated resources are pending.

**Deferred / cut** (documented, never silent — unresolved cases become
`uncertain` nodes, which is correct behavior): DB detection (JPA/JDBC) · OpenAPI
ingestion · Kafka Schema Registry (network) · gRPC · WebSocket · full K8s topology
· running `helm`/`kustomize` (we trace statically) · Spring Cloud Config Server +
secret managers · inter-procedural value analysis · interaction-style modeling.

**Coverage expectation** (priors, not measured): ~85–95% of edges on idiomatic
Spring MVC; lower on platform-heavy code (WebFlux functional routing, Spring Cloud
Stream). The resolution ceiling is externalized/runtime config, not parsing.

---

## Testing

```sh
go test ./...
```

Every package is tested: golden byte-stable output, per-detector tests through the
real query engine, the value-lattice algebra, config/Helm resolution, the schema
walker (nullability/requiredness/inheritance/cycles), the contract parsers, and
the auth/submit gate (via `httptest`, including fail-closed and key-masking
audits). cgo/tree-sitter builds are exercised end to end.

---

## How it was built

The tool was built as **24 small, independently-reviewable PRs**, each compiling
green with its own tests, each making the CLI visibly do more:

1–2 model + provider seam · 3–4 pipeline + CLI (empty JSON) · 5–6 tree-sitter +
query engine · 7 REST endpoints · 8–9 config + placeholder resolution · 10–11
Helm/K8s/`.env` layer · 12 Feign · 13–15 value resolver (lattice, evaluator,
reaching-defs) · 16–18 RestTemplate/WebClient/Kafka · 19–21 TypeIndex + REST
schema · 22 Kafka schema · 23–24 auth + submit.

Each PR is a branch (`pr1-…` … `pr24-submit`) for commit-by-commit review.
