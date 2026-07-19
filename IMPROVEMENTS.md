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
| 19 | **`@Controller` + `@ResponseBody` style invisible**: ALL 31 controllers in `mall-admin` (80k-star repo) use class `@Controller` with `@ResponseBody` on each method — the classic pre-`@RestController` enterprise style. We find 0 of its endpoints. | round-5 / `macrozheng/mall` @ `0504e86` | Treat a `@Controller` class as a REST controller when the method (or class) carries `@ResponseBody`; everything else (path composition etc.) is already there. | **high** | ✅ implemented |
| 20 | **Functional routing (`RouterFunction`) invisible**: `route(GET("/posts"), handler)` chains declare endpoints in code, no annotations. 0 of 3 found. | round-5 / `hantsy/spring-reactive-sample` routes app | Detector for `GET/POST/...("path")` calls gated on the functional-web import; compose `nest()`/`path()` prefixes best-effort. (This was tier-5, previously skipped.) | high | ✅ implemented |
| 21 | **Helm `envFrom` + templated ConfigMap not traced**: real charts (aws retail-store) put config in a TEMPLATED ConfigMap and wire it with `envFrom` + `configMapRef` + `include` helpers. Our tracer only pairs literal `name:`/`value:` env entries. | round-5 / `retail-store-sample-app` cart chart | Parse `data:` blocks inside chart-template ConfigMaps (tolerant scan like the env tracer), resolving `{{ .Values.x }}` values; treat `envFrom` a ConfigMap in the same chart as importing those keys. | medium | ✅ implemented |
| 22 | **Cross-class constructor-argument flow**: OAuth boilerplate calls `getForEntity(path)` where `path` comes from a constructor argument set in ANOTHER class (`new CustomUserInfoTokenServices(userInfoUri, ...)`). Honest uncertain today. | round-5 / `piggymetrics` account-service | General inter-procedural/cross-class flow — big; only worth it if it shows up more often. Park as low. | low | ✅ implemented |
| 23 | **Cloud Stream function COMPOSITION**: `spring.cloud.function.definition: uppercase\|echo` composes beans — bindings are named `uppercase\|echo-in-0`, not per-bean defaults. We emit 4 wrong-name topics and miss the real ones. | round-7 / `spring-cloud-stream-samples` function-composition-kafka | Read `spring.cloud.function.definition`; when it composes (`a\|b`), emit bindings for the COMPOSED name (in from the first, out from the last) instead of per-bean defaults; also only emit beans that appear in the definition when one is set. | medium | |
| 24 | **Producer topic hidden behind a `NewTopic` bean + `KafkaHeaders.TOPIC` header**: the idiomatic JavaGuides/Spring producer never passes a literal topic — it injects a `@Bean NewTopic` (`TopicBuilder.name("${...}")`) and sends `MessageBuilder.withPayload(x).setHeader(KafkaHeaders.TOPIC, topic.name()).build()`. Our `send()` detector read arg0 as the topic → an anonymous `uncertain` producer edge with an EMPTY topic, so the async graph never connects to the consumers. | round-8 / `haphong463/springboot-kafka-microservices` @ `bbcd5b4` (order-service `OrderProducer`, product-service `ProductProducer`) | Index `@Bean NewTopic` methods (`TopicBuilder.name(<arg>)` arg node → `Index.TopicBeans`). In the `send` detector: 2+ args → arg0 is the topic (unchanged); a single `Message` arg → follow the local var to the `MessageBuilder` chain, read the `KafkaHeaders.TOPIC` header; when it is `<newTopicField>.name()`, resolve through the topic bean's name-arg + config layer, capped `likely`. No bean → still an honest uncertain edge. | high | ✅ implemented |
| 25 | **Shared lib consumed by GAV, not reactor**: `common-lib/` sits as a sibling dir but there is NO aggregator pom — each service is standalone and depends on `io.github.haphong463:common-lib:1.0.7` as a published artifact. The #6 shared-module walk keys on `../pom.xml` `<modules>`, so it bails → `OrderEvent`/`ProductEvent`/`ApiResponse` never enter the Types index → ALL Kafka payload schemas and most endpoint schemas are nil even though the source is in the checkout. | round-8 / `haphong463/springboot-kafka-microservices` @ `bbcd5b4` (all Kafka edges + endpoint DTOs) | In `collectSharedModules`: keep the reactor path; add a GAV path — parse the service pom's `<dependencies>`, parse each sibling dir's pom coordinates (groupId falls back to `<parent>`), and index siblings whose `groupId:artifactId` the service declares (artifactId exact; groupId exact unless either side is empty/`${...}`). Types only, detectors never see them — same rule as #6. | high | ✅ implemented |
| 26 | **Cross-class wrapper producer**: `EventProducer.send(topic, msg)` wraps `kafkaTemplate.send(topic, message)` — the topic is a method PARAM, and the call sites live in OTHER classes with resolvable `KafkaConstant.PROFILE_ONBOARDED_TOPIC` args. #13 unions call-site args intra-class only → both producer edges come out anonymous/uncertain while the constants sit in the repo. Thin producer wrappers are the norm in real codebases. | round-9 / `hoangtien2k3/ecommerce-microservices` @ `138c63e` (notification + payment `EventProducer`) | Mirror of #22: build a repo-wide method-call-site index (like `Types.CreationSites`) keyed by simple method name + receiver's declared type; `paramCallSites` falls back to it when the intra-class walk finds nothing. Union, capped likely, cycle-guarded. | high | ✅ implemented |
| 27 | **Nested aggregator shared module**: `common-lib` is listed in the root reactor but is ITSELF an aggregator (`common-core`, `common-kafka`, … 7 submodules). The sibling walk globs `src/main/java/**` directly under the sibling dir → finds zero files → no shared types, zero schemas across all 13 services. | round-9 / `hoangtien2k3/ecommerce-microservices` @ `138c63e` (all services, `common-lib/*`) | In `collectSharedModules`: when a qualifying sibling's pom has `<modules>`, recurse one level into its listed modules (same types-only rule). Applies to both the reactor and GAV paths. | high | ✅ implemented |
| 28 | **Debezium outbox producers invisible**: services "produce" by inserting an `OutBox` row — zero `KafkaTemplate` in the repo. The topic materializes in repo-root Kafka-Connect configs: `outbox_*_connector.json` has `transforms.outbox.route.topic.replacement = ${routedByValue}.events` routed by `aggregate_type`, and the aggregate types ARE static in code (`.aggregateType(ORDER)` constants). Consumers were found (cloudstream #14) but all producer edges are missing → the graph has consumers of `ORDER.events` with no producer. | round-9 / `uuhnaut69/saga-pattern-microservices` @ `41da497` (order/customer/inventory) | Parse repo-root `*connector*.json` Kafka-Connect configs: outbox SMT route pattern + route-by field → join with `aggregateType(<literal/constant>)` values found in the service's builder calls → one producer edge per aggregate type (`ORDER.events`), likely; unresolvable route values → uncertain edge per connector. | medium | ✅ implemented |
| 29 | **Gradle multi-module repos have no shared-type discovery**: FTGO has no pom.xml anywhere — `settings.gradle` lists 29 modules; sibling `*-service-api` modules hold the channel constants (`OrderServiceChannels.COMMAND_CHANNEL = "orderService"`) and DTOs. Our shared-module walk is pom-only → Symbols/Types never see them → messaging channels unresolvable, schemas nil. Gradle is roughly half of enterprise Java. | round-9 / `microservices-patterns/ftgo-application` @ `558dfc5` | Parse `settings.gradle(.kts)` `include` lines for the module list; read the service's `build.gradle` `project(':x')` dependencies for the GAV-equivalent filter; feed qualifying siblings to the same types-only indexing. No Gradle execution — text-parse only, mirroring the pom paths. | high | ✅ implemented |
| 30 | **Eventuate Tram messaging** (`DomainEventPublisher.publish(...)`, `MessageConsumer`/saga channels): 101 publish + 53 consume sites in FTGO, all invisible — the framework hides Kafka entirely. Channels are constants in `*-api` modules (needs #29 first). | round-9 / `microservices-patterns/ftgo-application` @ `558dfc5` | After #29: a small Eventuate detector — `DomainEventPublisher.publish(aggregateType, …)` = producer edge on the aggregate channel; `DomainEventHandlersBuilder.forAggregateType("x")` = consumer edge. Or leave to the `.ekg-adapters.json` mechanism (#15) and document the recipe. | low | |

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

## Round 5 result (2026-07-11) — new, more realistic repos; predictions pre-registered
Scores: piggymetrics account 4/4 endpoints + 2/2 Feign (+1 honest unknown);
stream-samples multi-functions-kafka 4/4 topics (validates #14 on real code);
retail-store cart 15/15 endpoints; petclinic-reactive 31/31 (Mono/Flux fine).
Misses that became findings: mall-admin 0% (R5→#19, @Controller+@ResponseBody —
predicted), functional routes 0/3 (#20 — predicted), Helm envFrom/templated
ConfigMap (#21 — predicted). Predictions that were WRONG: piggymetrics master
has no messaging at all (old-Cloud-Stream + rabbit-binder predictions n/a), and
--config-repo made no visible difference on account-service (no ${} edges there).

## Round 6 result (2026-07-11)
#19 (@Controller+@ResponseBody), #20 (functional routing), #21 (templated
ConfigMap / envFrom trace) implemented. Round-5 repos re-run: everything now
100% recall (mall sample 2/2, routes 3/3, all others unchanged). Bonus: the
new #20 detector found a REAL RouterFunction endpoint (GET /) in the old
tier-2 api-gateway that hand-labeling had missed — label corrected. Open: #22
(cross-class ctor-arg flow, low). compare.py gained a "sampled" labels mode
for big repos.

## Round 7 result (2026-07-11)
#22 implemented: fields set from plain constructor params now resolve through
`new ClassName(...)` sites across the repo (union, capped likely; opaque args
stay honest holes). Memo keys are now (file, offset) — cross-file safe.
Statistical-weight targets added (6 more): mall-portal 6/6 (sampled — second
@Controller+@ResponseBody module), classic petclinic NEGATIVE CONTROL 1/1
(view methods correctly excluded), piggymetrics statistics 3/3 + rates-client
Feign URL resolved ONLY via --config-repo (#16 validated on real code),
retail orders 9/9, boot-start-routes 5/5. New finding #23: Cloud Stream
function composition (4 wrong-name topics on function-composition-kafka).
Benchmark now spans ~26 targets across 5 tiers.

## Round 8 result (2026-07-19) — new Kafka-rich, no-cloud-config target
Added `haphong463/springboot-kafka-microservices` @ `bbcd5b4` (8 modules, Java,
MIT, Eureka-only — no config server). Clean recall on the parts we already model:
REST endpoints 32/32 confirmed (order 5, payment 1, product 22, identity 4),
Feign 5/5 confirmed (raw Eureka names; `STOCK-SERVICE` correctly an honest
unresolved edge — no such module), Kafka consumers 3/3 resolved `likely`
(`order_topics`, `create_order_topic` via `@KafkaListener("${...}")`). New finding
#24 (producer `NewTopic`-bean + `KafkaHeaders.TOPIC` indirection) found AND
implemented same round: order-service producer now resolves `create_order_topic`,
product-service `product_topics` (both `likely`) — previously anonymous/uncertain.
Fixtures `TestKafkaProducerNewTopicBeanHeader` / `...MessageNoBeanUncertain` added.
Live-backend re-submit diffed +1/−1 on both (resolved edge replaces the empty one).
Still open: api-gateway 0 edges — Spring Cloud Gateway `uri: lb://SERVICE` routes
live in `application.yml`, no code; decide whether config-route outbound deps are
in scope (7 routes to IDENTITY/PRODUCT/ORDER unseen today).

Follow-up, same round: #25 (GAV-consumed shared lib) found and implemented — the
repo has no aggregator pom, so the #6 walk never saw `common-lib`. With the GAV
path, every Kafka edge now carries its payload schema (`OrderEvent` with nested
`OrderDTO` → `array<OrderItemDTO>` at depth 2, `ProductEvent`), and endpoint
schemas lit up too (`ApiResponse` + request DTOs across order/product). Live
re-submit diffs: 6/2/1/20 "changed" on order/payment/email/product — all schema
fills, zero edge churn. Fixture `TestSharedLibraryByGAV` (positive + negative
sibling) added next to `TestSharedModuleTypes`.

## Round 9 result (2026-07-19) — four repos, four new stressors; scans only, fixes pending
Targets (35 services scanned, all exit 0): `SelimHorri/ecommerce-microservice-backend-app`
@ `695a6d4` (10-svc REST mesh), `uuhnaut69/saga-pattern-microservices` @ `41da497`
(Debezium outbox, Java 21), `hoangtien2k3/ecommerce-microservices` @ `138c63e`
(13 svcs + nested common-lib, reactive), `microservices-patterns/ftgo-application`
@ `558dfc5` (Gradle + Eventuate, 3.7k★).
What VALIDATED on new real code: RestTemplate + constant-host templates (#3/#10) —
SelimHorri's 7 cross-service edges all resolved `http://USER-SERVICE/user-service/api/users/{?}`
likely; 149 endpoints across the mesh incl. proxy-client 71; Cloud Stream detector (#14)
found all 4 saga consumers (`ORDER.events` etc.) with zero Spring-Kafka annotations;
@KafkaListener via constants and `${...}` both resolved (hoangtien2k3); Spring MVC
endpoint extraction works unchanged on a Gradle repo (FTGO 17 endpoints); negative
controls clean (cloud-config / service-discovery / gateways = 0 edges).
New findings: #26 (cross-class wrapper producer — the only 2 producer misses in
hoangtien2k3), #27 (nested aggregator shared module — kills ALL schemas there),
#28 (Debezium outbox producers — graph shows consumers with no producer),
#29 (Gradle shared-type discovery), #30 (Eventuate, gated on #29, parked low).

## Round 9 fix result (2026-07-19) — #26–#29 implemented, plus two found-while-fixing
All four implemented and re-verified against the live repos + backend:
saga now emits ALL THREE outbox producers (`ORDER/CUSTOMER/PRODUCT.events`,
likely) — the choreography loop closes end to end; hoangtien2k3 wrapper
producers resolve to their real topics; FTGO endpoint schemas resolve from the
Gradle `-api` modules (`CreateOrderRequest`/`GetOrderResponse` etc.);
hoangtien2k3 endpoint schemas 15/15 via the nested-aggregator walk.
Found while fixing (both fixed + tested):
- | 31 | **Sibling-service value leak**: under a reactor the shared set includes
  sibling SERVICES; following wrapper call sites through THEIR code injected
  notification's topic into payment's graph (same-named `EventProducer`). Fix:
  shared files feed the Types index as TYPE DEFINITIONS ONLY — creation/call
  sites are indexed for service-owned files exclusively. | ✅ |
- | 32 | **Multi-document application.yml**: `yaml.Unmarshal` read only the
  first `---` document; Spring applies all of them. search-service's
  `product.topic.name` lived in doc 3 → consumer was anonymous. Fix: decode all
  documents, merge unmarked ones in order, skip profile-marked overlays. | ✅ |
Fixtures: TestKafkaProducerCrossClassWrapper, TestKafkaProducerWrapperUnrelatedReceiver,
TestNestedAggregatorSharedModule, TestKafkaOutboxProducer (+setter +no-connector
gates), TestGradleSharedModule, TestSharedSiblingServiceNoValueLeak,
TestParseYAMLMultiDocument. Full suite green, zero regressions on rounds 1–8.
Backend note: live re-submits exposed that the ingest backend keys baselines by
service_id ONLY — order-service from different repos overwrite each other
(round-8 haphong463 vs round-9 repos). Backend-side fix needed: scope the
baseline by (repository, service_id).
