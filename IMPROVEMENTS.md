# Improvements found during benchmark testing

Findings from testing against real open-source repos. We implement these
AFTER the testing phase is done. Each entry says where it was found and what
to do.

| # | Finding | Found in | What to do | Priority | Status |
|---|---------|----------|------------|----------|--------|
| 1 | Controllers that implement **OpenAPI-generated interfaces**: the mapping annotations (`@GetMapping` etc.) are not in the source — they are generated at build time from `src/main/resources/openapi.yml`. We find ~1 of ~40 endpoints there. | tier-1 / `spring-petclinic-rest` @ `c7b5f5e` | Add an OpenAPI reader: when a `@RestController` implements an interface we cannot find in the source AND an `openapi.yml`/`openapi.json` exists, read endpoints (+ schemas) from that file. This partly reverts the "OpenAPI cut from MVP" decision — the pattern is real and common. | high | ✅ implemented |
| 2 | **Silent edge loss** (breaks our honesty rule): a WebClient `.uri(field + "path")` where the host part is unknown becomes a Template whose FIRST segment is a hole. Our "skip bare-path uri" rule drops it completely — no `uncertain` edge is emitted. The dependency disappears without a trace. | tier-2 / `spring-petclinic-microservices` @ `305a1f1` (api-gateway → visits, genai → customers) | In `detect_webclient.go` `emitURI`: a bare LITERAL path (`/pay`) is safe to skip, but a Template that STARTS with a hole may hide a host — emit an `uncertain` edge instead of skipping. | **high** | ✅ implemented |
| 3 | **Instance-field initializer not followed**: `private String hostname = "http://visits-service/";` used later in `.uri(hostname + "...")`. The value is right there in the class, but the evaluator only reads `@Value` fields and `static final` constants. | tier-2 / api-gateway `VisitsServiceClient` | In the Java evaluator (`evalName`/`evalFieldAccess`): when the name is an instance field of the enclosing class with a literal (or resolvable) initializer, evaluate that initializer. | high | ✅ implemented |
| 4 | **DiscoveryClient pattern**: `discoveryClient.getInstances("customers-service").get(0).getUri()` — the service name is a string literal in the code, but it sits inside a helper method (needs inter-procedural) and a framework API we do not model. | tier-2 / genai-service `AIDataProvider` | Two options: (a) special-case `DiscoveryClient.getInstances("<name>")` as a service-name source, like `@FeignClient(name=...)`; (b) general inter-procedural following (bigger). Option (a) is cheap and matches how Feign names are already emitted raw. | medium | ✅ implemented |
| 5 | **Kafka Streams not detected**: `builder.stream("payment-orders")` consumes and `.to("orders")` produces, but we only look for `KafkaTemplate.send` / `@KafkaListener`. All 3 stream topics in order-service were silently missed. | tier-3 / `sample-spring-kafka-microservices` @ `4e1ed6b` (order-service `OrderApp`) | Add a Kafka Streams detector: `StreamsBuilder.stream(topic)` / `.table(topic)` = consumer edge, `KStream.to(topic)` = producer edge. Topic args go through the same value resolver. | high | ✅ implemented |
| 6 | **Shared-module DTO not found**: the Kafka payload class `Order` lives in a sibling Maven module (`base-domain/`), outside the scanned service folder — so the schema is nil even though the type is in the same repo. Common enterprise pattern (shared contracts module). | tier-3 / all 3 services (payload `pl.piomin.base.domain.Order`) | Option: when the service's `pom.xml` declares a dependency on a sibling module in the same repo, also index that module's types (types only — do not detect edges there). | medium | ✅ implemented |
| 7 | **Duplicate identical edges**: two `send("orders", ...)` call sites produce two identical `orders` producer edges in the output. Harmless (backend dedups by topic+direction) but noisy. | tier-3 / order-service | Dedup identical edges in the marshal/sort step. | low | ✅ implemented |

| 8 | **@Value on constructor parameters not followed**: `LedgerWriterController(@Value("http://${BALANCES_API_ADDR}/balances") String balancesApiUri)` assigned to a field. The evaluator reads @Value only on FIELD declarations, so the value stays unknown. Constructor injection is the idiomatic modern style. | tier-4 / `bank-of-anthos` @ `6e5499f` (ledgerwriter) | In the evaluator's field lookup: when a field has no @Value/initializer, check the constructor for a parameter with @Value that is assigned to this field (`this.x = x`). | high | ✅ implemented |
| 9 | **Deploy config outside the service root**: in monorepos the k8s manifests live at the REPO root (`kubernetes-manifests/config.yaml` holds `BALANCES_API_ADDR`), but we only glob under the scanned service folder — the deploy layer never sees them. | tier-4 / `bank-of-anthos` (all Java services) | Discover repo-root deploy config like shared modules (walk up to the repo root, glob k8s/helm dirs), and/or wire the planned `--deploy-glob` flag (was skipped in PR 11 as unnecessary — the real world disagrees). | high | ✅ implemented |

## How to add a new entry
When a benchmark tier finds a miss: add a row here with the repo + commit SHA,
and if possible add a small unit-test fixture in the extractor that reproduces
the miss (so the fix later has a failing test ready).

## Round 1 result (2026-07-10)
All 7 findings implemented and verified by re-running the benchmarks:
every category in every tier-1/2/3 repo now scores **100% precision / 100% recall**
(the one remaining "gap" is a genuinely runtime path parameter, correctly
emitted as an honest `uncertain` edge). petclinic-rest went 1 → 38 endpoints.

## Round 2 result (2026-07-10)
#8 (@Value on constructor params) and #9 (repo-root deploy config, strict
k8s-only walk-up discovery) implemented. bank-of-anthos ledgerwriter's outbound
went from `{?}/{?}` to `http://balancereader:8080/balances/{?}` — constructor
@Value resolved through the repo-root ConfigMap; only the runtime account id
stays a hole. All tiers re-run: 100%/100% everywhere, no regressions.
