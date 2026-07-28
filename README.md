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
- **Two scan modes** (`--mode`) — `scan-repo` (default) turns a *service* repo
  into its architecture graph; `deploy-repo` turns a *deployment/GitOps* repo
  (Helm charts, Kustomize overlays, plain k8s manifests) into a service-identity
  map the backend uses to resolve which service actually owns each host. See
  [Host & identity resolution](#host--identity-resolution-deploy-repo-mode).

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

## Host & identity resolution (deploy-repo mode)

A `scan-repo` run resolves an outbound call to a **host string** (e.g.
`PYM_URL` → `pym-service`) but can't prove *which* scanned service owns that
host — repo name, logical service name, and DNS host routinely diverge
(`payments-area` repo → `payments-engine` app → `pym-service` host). That
mapping lives in the deployment repo, not the service repo. `--mode=deploy-repo`
scans that repo and emits an **identity map** — `service_name` → `hosts[]`
facts — that the backend joins against those unresolved edges to complete them.

Host resolution is a set of **selectable resolvers** (`--resolvers`, default =
all), so a company enables the mechanisms matching its infrastructure:

| Resolver | What it reads |
|---|---|
| `helm` | real Helm chart rendering (`helm.sh/helm/v3` — `_helpers.tpl`, `fullname`, per-`values-<env>.yaml` overlay), offline |
| `kustomize` | real overlay build (`sigs.k8s.io/kustomize/api` — `namePrefix`/`nameSuffix`, base composition) |
| `k8s-raw` | plain, already-rendered `Service` / `Ingress` / `VirtualService` manifests |
| `ingress` / `istio` | cross-cutting toggles: whether `Ingress` / Istio `VirtualService` external hosts are folded in |
| `self-declared` | a committed `.ekg-identity.json` fallback, for estates with no parseable deploy repo |

Rendering is embedded (Go libraries, no shelling out to `helm`/`kustomize`
binaries) and hermetic — only values/overlays committed in the repo are read,
never a cluster or a live chart-repo pull. A render failure on one chart/overlay
is a non-fatal warning, never aborting the scan.

Each host carries **provenance and a match class** so the backend knows how to
join it:

- **`in-cluster`** — a k8s DNS form (`svc`, `svc.ns`, `svc.ns.svc.cluster.local`);
  matched after normalizing a caller's FQDN down to the bare service name (the
  suffix is mechanical).
- **`external`** — an opaque hostname from an Ingress/gateway (`api.co.com/pay`);
  matched **exactly**, since it has no algorithmic tie to the service name.

Namespace is best-effort (the primary join key is the bare service name):
emitted when a manifest declares it, otherwise `default` — or derived from the
service name via `--namespace-convention` (`service-name` | `replace:<from>:<to>`)
for orgs whose namespace follows a convention.

```sh
# scan a GitOps repo, all resolvers, print the identity map
extractor --mode deploy-repo --root ./deploy-repo --api-key "$EKG_API_KEY" --dry-run

# only Kustomize, and derive namespace from the service name
extractor --mode deploy-repo --root ./deploy-repo --api-key "$EKG_API_KEY" \
  --resolvers kustomize --namespace-convention service-name --dry-run
```

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
cmd/extractor          CLI entrypoint (dispatches --mode)
internal/pipeline      scan-repo: auth-gate → detect → collect → parse → index → detect → schema → marshal → submit
internal/deployrepo    deploy-repo: auth-gate → selected resolvers → identity map → marshal → submit
internal/model         the JSON output contract + deterministic identity/sort (Service + IdentityMap)
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
Server / secret managers, version history. For deploy-repo host resolution:
**Terraform** (external DNS / load balancers / Cloud Map), **runtime service
registries** (Consul/Eureka — no static footprint), and richer Istio semantics
(`DestinationRule` subsets, weighted/mirror routes) are the next resolvers, not
yet built.

> Note: `scan-repo` config resolution reads `helm`/`kustomize` values
> *statically* (as flat config, for placeholder resolution). `deploy-repo` mode
> *fully renders* them via embedded Helm/Kustomize libraries to extract service
> identity — the one place the tool executes chart/overlay templates.

See [CLAUDE.md](CLAUDE.md) for the full spec this project is built against.
