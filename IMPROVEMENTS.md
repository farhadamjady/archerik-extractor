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
| 10 | **Known-host templates over-penalized**: `http://balancereader:8080/balances/{?}` was `uncertain`/unresolved even though the target SERVICE is fully known — only a runtime path/query value is missing. Endpoints already treat path variables as confirmed; the outbound side should match. (User review finding.) | tier-2 genai + tier-4 ledgerwriter results | In `emitTargets`: a Template whose first hole comes only AFTER a complete host (`"/" after "://"`) is `resolved`/`likely` with the `{?}` kept in the URL. A hole in the host itself stays `uncertain`. HTTP-only — a partial Kafka topic name stays uncertain (the name IS the identity). | medium | ✅ implemented |
| 11 | **Config bean getters not followed**: `props.getPaymentUrl() + "/pay"` where `props` is a `@ConfigurationProperties(prefix="payment")` bean — THE standard style in big companies. The host resolves to a hole today. | analysis of remaining host-hole cases (2026-07-11) | Type-directed, no data-flow needed: the receiver's type is in the TypeIndex → read `@ConfigurationProperties(prefix)` on that class → getter `getPaymentUrl` = property `payment.payment-url` → feed the existing config resolver. Probably the highest-value remaining improvement. | high | ✅ implemented |
| 12 | **`System.getenv("KEY")` not looked up**: the env-var NAME is a literal in the code, and we already have the deploy layer (k8s env / Helm / .env) that knows these keys — but the evaluator treats getenv as opaque. | same analysis | Special-case `System.getenv("<literal>")` (and `System.getProperty`) in the evaluator: resolve the key through the deploy/config layer, capped at likely. Small change. | medium | ✅ implemented |
| 13 | **Helper-method parameters not followed**: `call(host, path)` wrappers — the value flows in from the call sites. We follow a helper's RETURN (round 1) but not its ARGUMENTS. | same analysis | The mirror of return-inlining: find the method's call sites, evaluate the argument at each, union the results (one edge per candidate). Cheap within one class; cross-class needs a call index + bounds. Do intra-class first. | medium | ✅ implemented |
| 14 | **Spring Cloud Stream not detected**: `@Bean Consumer<Order> input()` + `spring.cloud.stream.bindings.input-in-0.destination=orders` — the standard event-driven style in many enterprises. We only see KafkaTemplate/@KafkaListener/Streams. | enterprise-gap analysis (2026-07-11) | Detect @Bean methods returning Consumer/Supplier/Function; binding name = `<method>-in-0`/`-out-0`; destination from `spring.cloud.stream.bindings.<binding>.destination` (default = binding name). Payload schema from the generic type. Gate on spring.cloud.stream config existing. | high | ✅ implemented |
| 15 | **Internal SDK / wrapper libraries**: `platformClient.call("payment-service", ...)` — the wrapper lives in a jar, so call sites do not look like HTTP at all. Silent miss, common in platform-heavy companies. | same analysis | Per-company adapter file committed in the repo (`.ekg-adapters.json`): `{method, target_arg, protocol}` entries; a generic detector resolves that argument through the value resolver and emits the edge. Not solvable generically — this makes it configurable. | medium | ✅ implemented |
| 16 | **Spring Cloud Config Server**: URLs/topics live in a SEPARATE config git repo; everything resolving through it stays uncertain. Very common in big enterprises. | same analysis | `--config-repo <path>`: point at a local checkout of the config repo; its yml/properties files feed the existing resolver as a lower-precedence layer. | high | ✅ implemented |
| 17 | **Meta-annotations not resolved**: platform teams compose `@MyGetMapping` / `@MyController` from Spring annotations; controllers using them are invisible. | same analysis | Index `@interface` declarations found in the repo; resolve ONE level: an unknown annotation whose declaration carries @RestController/@GetMapping/@RequestMapping behaves like it. | high | ✅ implemented |
| 18 | **`@HttpExchange` declarative clients (Spring 6)**: like Feign but newer and growing fast — `@GetExchange("/pay")` interfaces. Invisible today. | same analysis | New detector, same shape as Feign: one outbound edge per interface; target = interface-level @HttpExchange url (resolved via config) or the interface name as the raw logical name. | high | ✅ implemented |

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

## Round 3 result (2026-07-10)
#10 implemented: known-host templates are resolved/likely. The benchmark now has
ZERO gap entries — every labeled edge in all 4 tiers is found and resolved:
100% precision / 100% recall across 13 services.

## Round 4 result (2026-07-11)
#11-#18 implemented in one round: config-bean getters (@ConfigurationProperties),
System.getenv lookup via the deploy layer, helper-argument union from call sites,
Spring Cloud Stream bindings (config-gated), the .ekg-adapters.json wrapper
mechanism, --config-repo (external Spring Cloud Config checkout), one-level
meta-annotation resolution, and @HttpExchange clients. Full benchmark re-run:
100%/100% on all 13 services, zero regressions from the broadened controller
query and the three new detectors.
