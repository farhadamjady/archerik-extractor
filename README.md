# service-discovery

**Read a service's repository, get its architecture as JSON.** The REST
endpoints it exposes, the services it calls, the Kafka topics it produces and
consumes, and the declared request/response and message schemas behind them.

Static analysis only — no LLM, no code execution, no network. Point it at a
checkout and it prints a graph:

```sh
extractor --root ./order-service
```

```json
{
  "service_id": "order-service",
  "service_name": "order-service",
  "repository": "github.com/acme/order-service",
  "language": "Java",
  "endpoints": [
    { "method": "GET",  "path": "/api/orders/{id}", "response": { "type": "Order", "…": "…" },
      "protocol": "rest", "detection": "annotation", "confidence": "confirmed" },
    { "method": "POST", "path": "/api/orders",      "request":  { "type": "Order", "…": "…" },
      "protocol": "rest", "detection": "annotation", "confidence": "confirmed" }
  ],
  "outbound_dependencies": [
    { "target_name": "payment-service", "url": "http://payment-service:8080",
      "protocol": "rest", "detection": "feign", "confidence": "likely",
      "resolved": true, "resolved_via": "application.yml" }
  ],
  "kafka_producers": [
    { "topic": "orders.v1", "resolved": true, "schema": { "type": "Order", "…": "…" },
      "protocol": "kafka", "detection": "kafka", "confidence": "likely",
      "resolved_via": "application.yml" }
  ],
  "kafka_consumers": [ "…" ],
  "config_dependencies": [
    { "key": "orders.topic", "value": "orders.v1", "resolved": true,
      "confidence": "likely", "resolved_via": "application.yml" }
  ]
}
```

Note what happened in that example: the Feign client was declared as
`url = "${payment.service.url}"` and the Kafka topic came from a
`@Value("${orders.topic}")` field. Neither is a literal in the source. Both were
resolved through the service's config, and each edge says so — `likely`, not
`confirmed`, with the file that supplied the value.

Run it in CI on every commit and you have a per-commit architecture graph for
every service in the fleet.

## Why this is harder than grepping for annotations

Finding `@RestController` is easy. The reason a naive scan produces a graph
nobody trusts is that **targets are almost never hardcoded**:

- **Config indirection.** URLs and topics hide behind placeholders. The
  extractor resolves them through a layered config source — application
  config (with active profiles) → Helm `values*.yaml` traced through chart
  template `env:` blocks → rendered Kubernetes ConfigMap/Deployment env →
  `.env` files — unified by relaxed binding, so `payment.service.url` and
  `PAYMENT_SERVICE_URL` are the same key. Deployment config is read
  **statically as text**; `helm` and `kustomize` are never executed.
- **Path composition.** An endpoint path is the class-level mapping plus the
  method-level one. Method paths are never emitted in isolation, and path
  variables are preserved.
- **Values that are computed.** String concatenation, builders, and variables
  are followed where they can be followed (constants, `@Value` fields,
  reaching definitions, call-site unions) — and where they can't, the edge is
  still emitted, marked `uncertain`.

That last point is the design rule the whole project bends around: **an edge we
cannot resolve is emitted honestly rather than dropped or guessed.** A graph
that quietly omits what it couldn't figure out is worse than one that admits it.

## Supported stacks

| Language | Framework | REST endpoints | Outbound HTTP | Kafka |
|---|---|:--:|:--:|:--:|
| Java | Spring Boot | ✅ | ✅ Feign · RestTemplate · WebClient · `@HttpExchange` · WebMvc.fn | ✅ `KafkaTemplate` · `@KafkaListener` · Cloud Stream · reactor-kafka |
| Java | Micronaut | ✅ | ✅ `@Client` | ✅ `@KafkaClient` / `@KafkaListener` |
| Java | Quarkus | ✅ JAX-RS | ✅ `@RegisterRestClient` · JAX-RS client | ✅ reactive messaging |
| Kotlin | Spring Boot | ✅ | ✅ | ✅ |
| TypeScript | NestJS | ✅ | ✅ axios · fetch | ✅ |
| JavaScript | Express | ✅ call-based routing | ✅ axios · fetch | — |
| C# | ASP.NET Core | ✅ attribute routing + Minimal APIs | ✅ `HttpClient` · Refit | ✅ Confluent.Kafka |
| Go | `net/http` (no framework) | ✅ incl. Go 1.22 method patterns | ✅ std-lib client | ✅ kafka-go · sarama · confluent |

Request/response and message **schemas** are attached wherever the declared
types resolve — walked from the source DTOs, two levels deep by default
(`--schema-depth`), with containers unwrapped (`List<User>` → array of `User`,
`Optional<String>` → nullable string, `Page<Invoice>` → `Invoice`). For Kafka,
contract files in the repo (`.avsc`, `.proto`, JSON Schema) win over in-code
types when both exist.

Each language has its own tree-sitter parsing layer and each framework is a
provider on top of it, so adding a framework to an existing language is one
package and one registry line. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Install

Requires **Go 1.26+ and a C toolchain** — the tree-sitter grammars are C, so
cgo must be enabled.

```sh
go install github.com/farhadamjady/service-discovery/cmd/extractor@latest
```

Or from a checkout:

```sh
git clone https://github.com/farhadamjady/service-discovery
cd service-discovery
go build -o extractor ./cmd/extractor
```

## Usage

```sh
# print the graph to stdout
extractor --root ./my-service

# write it to a file, resolving the staging Helm overlay and prod profile
extractor --root ./my-service --out graph.json --environment staging --profiles prod
```

The framework is detected from the repo; there is nothing to configure for a
standard layout.

### Flags

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | Repository root to scan |
| `--out` | `-` | Output path for the JSON, or `-` for stdout |
| `--repository` | auto | Repo identifier emitted as `repository`; auto-detected from CI env or the git remote |
| `--profiles` | | Comma-separated active Spring profiles |
| `--environment` | | Deploy overlay to resolve, e.g. `staging` |
| `--config-repo` | | Local checkout of an external config repo whose yml/properties feed resolution |
| `--schema-depth` | `2` | Nested-DTO walk depth before truncation |
| `--config` | | Config file holding the API key (`api_key = <value>`) |
| `--api-url` | | Control-plane base URL — enables key validation and submission |
| `--api-key` | | API key for `--api-url` runs; also `EKG_API_KEY` |
| `--dry-run` | `false` | Produce the JSON but do not submit |
| `--branch`, `--sha`, `--pr` | auto | Commit metadata; auto-detected on GitHub Actions |
| `--comment-out` | | Write the backend's PR-comment markdown to this file |

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Runtime error (parse, IO, usage) |
| `2` | Detection failed — no provider matched the repo, or the match was ambiguous |
| `10`–`14` | Auth: missing key · invalid · not entitled · quota exceeded · validation server unreachable |
| `20` | Submission failed |

Detection failing loudly (exit 2) is deliberate: an unrecognized repo should not
quietly produce an empty graph that looks like a service with no dependencies.

### In CI

```yaml
- uses: actions/setup-go@v5
  with: { go-version: '1.26' }
- run: go install github.com/farhadamjady/service-discovery/cmd/extractor@latest
- run: extractor --root . --out architecture.json
- uses: actions/upload-artifact@v4
  with: { name: architecture, path: architecture.json }
```

Every commit runs a **full scan of the service**, not a scan of the changed
files — a commit's architectural impact is not confined to its diff (a config
edit ripples into edges in files it never touched, and deletions have to remove
nodes). The output is **byte-stable**: identical input always produces identical
bytes, so a consumer can diff commit-to-commit reliably.

## Output contract

One JSON object per service, with these top-level fields: `service_id`,
`service_name`, `repository`, `language`, `endpoints`,
`outbound_dependencies`, `kafka_producers`, `kafka_consumers`,
`databases_used`, `config_dependencies`.

Every communication edge carries three **orthogonal** fields, so the graph is
queryable along any of them:

- **`protocol`** — what it is: `rest` · `kafka` · `grpc` · `websocket` · `unknown`.
  Feign, RestTemplate, and WebClient are three detections of *one* protocol.
- **`detection`** — how it was found: `annotation`, `feign`, `resttemplate`,
  `webclient`, `httpexchange`, `cloudstream`, `router`, `micronaut-client`,
  `mp-rest-client`, `jaxrs-client`, `reactive-messaging`, `kafka`, `axios`,
  `fetch`, `http-client`, `dotnet-httpclient`, `refit`, `openapi`, `config`,
  `adapter`.
- **`confidence`** — how sure we are: `confirmed` (a literal value) ·
  `likely` (resolved through one config indirection) · `uncertain` (dynamic or
  unresolvable — still emitted, never dropped).

Two deliberate omissions:

- **`inbound_dependencies` is never emitted.** Inbound edges are derived by
  whoever aggregates the graphs, from everyone else's outbound edges. A service
  cannot know who calls it.
- **Logical names are emitted raw.** `@FeignClient(name="payment-service")` is a
  logical name, not a service ID. Mapping names to identities is the
  aggregator's job — the extractor does not guess it.

Identity keys are stable by design: an endpoint is `verb + path`, a dependency
is `target + detection`, a Kafka edge is `topic + direction`.

## Sending results somewhere

Extraction is free, local, and offline — everything above needs no key and no
network.

The CLI can also **submit** the graph to a control plane that stores it, derives
inbound edges across services, and diffs each commit against the last:

```sh
extractor --root . --api-url https://api.example.com --api-key "$EKG_API_KEY"
```

That path validates the key before scanning (so a rejected run costs nothing)
and the server re-validates at submit. Key precedence is `--api-key` >
`EKG_API_KEY` > config file; it is never logged.

`cmd/ekgd` in this repo is a **reference backend** implementing the ingest
contract — enough to run the whole loop locally, and a starting point if you
want to build your own. Hosting one is optional; the extractor is fully useful
without it.

## Scope

Deliberately **not** in the extractor, by design rather than omission: any LLM
inference, executing or building the scanned code, rendering Helm/Kustomize
templates, and uploading source code (only the derived JSON ever leaves).

Not implemented yet: database detection (JPA/JDBC — the field is present but
empty), gRPC, OpenAPI as a primary source (it is read only when the build
*generates* controllers from a spec), full Kubernetes topology, and runtime
config sources such as Spring Cloud Config Server or secret managers.

## Contributing

Bug reports about a missed or wrong edge are the most valuable contribution —
please include the source snippet and the JSON it produced. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the architecture, the invariants a change
must respect, and how to add a framework or a language.

## License

[MIT](LICENSE).
