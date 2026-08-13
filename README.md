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

Every engineering org has the same problem: nobody can say with confidence what
talks to what. The wiki diagram was accurate the week it was drawn. The only
artifact that tells the truth is the source code — and reading it by hand across
a few hundred services is not a plan.

Archerik reads it for you. Point the extractor at a service repository and it
produces a complete, machine-readable map of how that service participates in
your system:

- **REST endpoints it exposes** — full composed paths, HTTP verbs, path
  variables preserved.
- **Services it calls** — Feign, RestTemplate, WebClient, axios, `HttpClient`,
  Refit and more, with the target URL resolved through the service's real
  configuration.
- **Kafka topics it produces and consumes**, with the topic name resolved
  rather than left as `${orders.topic}`.
- **The declared schemas** behind each endpoint and message, walked from the
  source DTOs and contract files.

The hard part is not finding `@RestController` — it is that URLs and topic names
are almost never literals. Archerik resolves placeholders through a layered
config source (application config and active profiles → Helm `values*.yaml`
traced through chart template `env:` blocks → rendered Kubernetes ConfigMap and
Deployment env → `.env` files), unified by relaxed binding so
`payment.service.url` and `PAYMENT_SERVICE_URL` are the same key. Every edge
reports how confident it is and which file supplied the value — and an edge that
cannot be resolved is emitted honestly rather than dropped or guessed.

Wire it into CI and the map maintains itself, commit by commit, with no one
assigned to keep it up to date.

## The Archerik platform

Archerik is three components. This repository is the first one; it is useful on
its own, and the other two turn per-service output into a fleet-wide graph.

| Component | Repository | Role | Depends on |
|---|---|---|---|
| **Extractor** | [`archerik-extractor`](https://github.com/farhadamjady/archerik-extractor) *(this repo)* | Scans one service repository and emits its architecture as JSON. Runs locally or in CI. | — |
| **API** | [`archerik-api`](https://github.com/farhadamjady/archerik-api) | Ingests graphs from the extractor, stores them, derives inbound edges across services, and diffs each commit against the last. | Extractor output |
| **UI** | [`archerik-ui`](https://github.com/farhadamjady/archerik-ui) | Explores the fleet graph — services, dependencies, topics, and schemas — and visualizes what changed. | API |

The extractor never talks to the UI directly. It produces JSON; the API is the
only thing that consumes it, and the UI reads everything through the API.

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

## Install

Requires **Go 1.26+ and a C toolchain** — the tree-sitter grammars are C, so
cgo must be enabled.

```sh
go install github.com/farhadamjady/service-discovery/cmd/extractor@latest
```

Or from a checkout:

```sh
git clone https://github.com/farhadamjady/archerik-extractor
cd archerik-extractor
go build -o extractor ./cmd/extractor
```

The binary is `extractor` — the scanning component of Archerik, and the only
thing you need for everything below.

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
<sub><b>Archerik</b> — static architecture intelligence, straight from the source.</sub>
</div>
