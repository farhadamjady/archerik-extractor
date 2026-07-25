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
| 33 | **REST mappings live on an implemented interface in a sibling module** — the `@RestController` class carries NO mapping annotations; it `implements` a hand-written Java interface (`ProductService`, `ProductCompositeService`) in the shared `:api` Gradle module, and THAT interface's methods hold `@GetMapping`/`@PostMapping`/`@DeleteMapping` + `@PathVariable`/`@RequestParam`/`@RequestBody`. The endpoint detector only reads annotations on the controller class itself → **0 endpoints found** across all 4 core services. Distinct from #1 (OpenAPI-generated iface, read from `openapi.yml`) and #17 (meta-annotations): here the annotations are ordinary Spring, on a real interface that is IN the source (a shared module #29 already indexes for types). This "server-side interface = API contract" style is idiomatic Spring and very common. | round-10 / `PacktPublishing/Microservices-with-Spring-Boot-and-Spring-Cloud-Third-Edition` @ `e4820dd` (Chapter15, all 4 microservices) | When a `@RestController`/`@Controller` class implements one or more interfaces, resolve those interface types (in-service first, then shared modules already in the type index) and MERGE their class-level `@RequestMapping` + method-level mapping annotations onto the implementing methods (match by name+erased params). Path composition/verb/schema logic is unchanged once the annotations are found. | high | |
| 34 | **`StreamBridge.send(binding, msg)` producer not detected** — the imperative Spring Cloud Stream producer API. #14 detects `@Bean Supplier/Function`; StreamBridge is the other (more common for ad-hoc event publishing) half and is invisible → composite's 3 producer edges (`products`/`recommendations`/`reviews`) all missed. Binding→destination resolution is identical to #14. | round-10 / magnus `product-composite-service` (`ProductCompositeIntegration.sendMessage` → `streamBridge.send("products-out-0", msg)`) | Detect `StreamBridge.send(<arg0>, …)`: resolve arg0 (a binding name, often a literal or built from a constant) → `spring.cloud.stream.bindings.<binding>.destination` (default = binding name with the `-out-0` suffix stripped per Spring rules); emit a producer edge, payload schema from the `Message`/payload generic. Gate on spring.cloud.stream being on the classpath/config, same as #14. | high | |
| 35 | **Config-repo not scoped by `spring.application.name`** (correctness — emits WRONG edges, not just misses) — `addConfigRepo` flattens EVERY yml/properties file in the `--config-repo` checkout into one fallback map with first-writer-wins by sorted filename. But a Spring Cloud Config repo is keyed by application: service `recommendation` must read `recommendation.yml` (+ shared `application.yml`), NEVER `product.yml`. Every core service names its consumer bean `messageProcessor` (binding `messageProcessor-in-0`), so the shared key resolves to `product.yml`'s `products` for ALL of them → recommendation-service and review-service emit consumer topic **`products`** instead of `recommendations`/`reviews`, at `likely`. Silent cross-service contamination. | round-10 / magnus `recommendation-service` + `review-service` (both wrong via `config-repo/{product,recommendation,review}.yml`) | Scope config-repo file selection to the scanned service: read its `spring.application.name` (from in-repo config), then from the config repo load only `application.{yml,properties}` (shared, lowest precedence) + `<app-name>[-<profile>].{yml,properties}` (specific, higher), applying Spring precedence within that set. Ignore every other service's files. | high | |
| 36 | **WebClient `.uri(URI)` built via `UriComponentsBuilder.fromUriString(HOST_CONST + "/path…").build(args)`** — the host is a `static final String` (`"http://product"`), fully resolvable, but it flows through `UriComponentsBuilder` into a local `URI` variable, and `.uri(...)` takes that `URI` (not a String). The WebClient detector doesn't model `UriComponentsBuilder`/`.uri(URI)` → composite's 3 outbound REST edges (product/recommendation/review) collapse to a single anonymous `uncertain`. | round-10 / magnus `product-composite-service` (`ProductCompositeIntegration.getProduct/getRecommendations/getReviews`) | Model `UriComponentsBuilder.fromUriString(x)....build(...)` as carrying x's string value; follow a local `URI`/`String` var into `.uri(var)`; the `CONST + "/path"` concatenation already folds via the existing evaluator, so the host resolves and the `{productId}` etc. stay as `{?}` holes (known-host template, #10) → `likely`. | medium | |
| 37 | **`EurekaClient.getApplication(@Value applicationName)` discovery target not resolved** — auth-service calls profile-service by asking Netflix Eureka for the instance (`client.getApplication(applicationName)` where `applicationName = @Value("${com.amdocs.external.application.name}")` = `PROFILE-SERVICE`), then builds `"http://" + instanceInfo.getHostName() + ":" + port + "/profile"` and `restTemplate.exchange(url,…)`. The host/port are runtime method returns → we emit `http://{?}:{?}/profile` uncertain and lose the logical target. Like #4 (`DiscoveryClient.getInstances("literal")`) but the API is `EurekaClient.getApplication(...)` AND the arg is a `@Value` config property, not a literal. | round-11 / `omkarnikam24/springboot-microservices-kafka` @ `2d9e4b1` (auth-service `DelegationController`) | Special-case `EurekaClient.getApplication(<arg>)` / `.getNextServerFromEureka(<arg>)` as a service-name source (mirror #4): resolve `<arg>` through the value+config layer (here `${com.amdocs.external.application.name}` → `PROFILE-SERVICE`) and emit that as the outbound `target_name` (logical Eureka name, raw — backend maps it), `likely`; keep the `/profile` path. Unresolvable arg → uncertain, as today. | medium | ✅ implemented (Option A) |
| 38 | **Functional `RouterFunction` `.path(prefix, builder → builder.GET(...))` prefix not composed + dedup collapse** (correctness — emits WRONG paths, not just misses) — the #20 router detector finds the inner `.GET("/current")`/`.PUT(...)` verbs but ignores the enclosing `route().path("/accounts", b → …)` (and `nest(...)`) prefix. api-gateway declares 7 endpoints across three `.path()` groups (`/accounts/*`, `/notifications/*`, `/statistics/*`); we emit 3 prefix-stripped paths (`GET /current`, `GET /demo`, `PUT /current`) which then COLLIDE across groups and dedup — so `/accounts/current`, `/notifications/current`, `/statistics/current` become one wrong `GET /current`. 0/7 recall, 3 false positives. #20 flagged "nest()/path() prefixes not composed yet"; this is the concrete, high-value case. | round-12 / `galleog/piggymetrics-k8s` @ `ff2128a` (api-gateway `RouterConfig`) | In the router detector, track the enclosing `path("<prefix>", …)` / `nest(<pred>, …)` lambda(s) and prepend the prefix(es) to each inner verb path before emitting (compose, like class `@RequestMapping` + method mapping). The builder receiver flows into the lambda, so walk up from the verb call to its enclosing `path(...)` argument list. | high | |
| 39 | **Reactive Kafka (`ReactiveKafkaProducerTemplate` / `ReactiveKafkaConsumerTemplate`, reactor-kafka) not detected** — the whole event choreography is invisible: producers call `producerTemplate.send(SenderRecord.create(new ProducerRecord<>(topic, key, event), …))` (topic = `@Value("${spring.kafka.producer.topic}")`), consumers are `@Component class …Consumer implements Function<Flux<ConsumerRecord<K,V>>, Mono<Void>>` wired to a `ReactiveKafkaConsumerTemplate` whose subscription is `spring.kafka.consumer.subscribeTopics`. We only model `KafkaTemplate.send` / `@KafkaListener` / Streams / Cloud Stream → 0 producers, 0 consumers across account/statistics/notification (account→`account-events`→statistics; keycloak→`user-events`→account+notification). Standard Spring reactive Kafka, growing fast. | round-12 / `galleog/piggymetrics-k8s` @ `ff2128a` (all services + `pgm-autoconfigure`) | New reactor-kafka detector: producer = `ReactiveKafkaProducerTemplate.send(...)` — follow the arg to the `new ProducerRecord<>(<topicExpr>, …)` (through `SenderRecord.create`) and resolve `<topicExpr>` via the value+config layer; consumer topic = read `spring.kafka.consumer.subscribeTopics` for a service that has a `ReactiveKafkaConsumerTemplate<K,V>` + a `Function<Flux<ConsumerRecord<K,V>>,…>` bean, payload = V (unwrap `Flux<ConsumerRecord<K,V>>`). Payloads here are protobuf types (schema via #6/#29 shared-module + `.proto`). | high | |
| 40 | **gRPC sync mesh entirely invisible** (deferred milestone, per CLAUDE.md scope) — galleog is ~100% gRPC for synchronous calls: `@GrpcService` servers (AccountService/StatisticsService/RecipientService) and `@GrpcClient` stubs (api-gateway → account/statistics/notification; notification → account). None of the sync inter-service edges nor the gRPC "endpoints" are seen; the graph is 4 disconnected nodes. Not a bug — gRPC is explicitly out of MVP scope — but a strong, popular real-world argument for prioritizing the gRPC milestone (proto service/rpc defs are right there in `*/src/main/proto`, and `@GrpcClient("NAME")` carries the logical target like Feign). | round-12 / `galleog/piggymetrics-k8s` @ `ff2128a` | When the gRPC milestone lands: `@GrpcService` on a class implementing `<Svc>Grpc.<Svc>ImplBase` = inbound gRPC surface (protocol grpc); `@GrpcClient("NAME")` field / injected stub = outbound edge, target = the raw channel name (backend maps it); messages/rpcs from the `.proto` (already parseable). | low (deferred) | |
| 41 | **Micronaut API-interface controllers** (the #33 pattern, in Micronaut, where it is the DOMINANT style): the `@Controller` class carries only bare `@Override` methods; the HTTP mappings (`@Get`/`@Post` + path + `@Body`) live on a hand-written interface (`PolicyOperations`, `OfferOperations`) in a sibling `*-api` Maven module the detector never scans. Round-1 Micronaut found only 2 of 6 endpoints in `policy-service` (just the one non-interface `HelloController`). | round-1-java-micronaut / `asc-lab/micronaut-microservices-poc` @ `9871a2e` (policy-service, dashboard-service, policy-search-service — all controllers) | Added neutral `Index.HTTPContracts` (interface simple-name → its HTTP-mapped method nodes) built by a contract indexer that sees `Parsed` + sibling `Shared` modules; the REST detector reads the controller's `implements`/`extends` list and composes each inherited interface method with the controller base path. Machinery is framework-neutral — **this now also unblocks #33 for Spring** (wire Spring's rest detector to consume `HTTPContracts`). | high | ✅ implemented (Micronaut) |
| 42 | **Kotlin Micronaut services out of scope**: `documents-service` is written in Kotlin (`src/main/kotlin/*.kt`, `@Controller`+`@KafkaListener`) — the Java provider parses only `**/*.java`, so it emits an empty graph while still matching (build file has `io.micronaut`). Honest but incomplete; the service's real endpoints/consumer are invisible. | round-1-java-micronaut / `asc-lab/micronaut-microservices-poc` @ `9871a2e` (documents-service) | Needs a Kotlin language layer (GUIDELINE Recipe B): a `lang/kotlin` tree-sitter parser + evaluator + type source, then a Kotlin-Micronaut provider reusing the same detectors (annotations are identical). Larger effort; deferred. Meanwhile a Kotlin-only service should arguably score lower/not match so detection can fail loud rather than emit empty. | medium (deferred) | |
| 44 | **Quarkus reactive-messaging (`@Incoming`/`@Outgoing`/`@Channel`) not detected** — Quarkus/SmallRye messaging is the standard Kafka style: `@Incoming("channel")` (consumer), `@Outgoing("channel")` (producer), and `@Channel("channel") Emitter<T>` (producer). The channel is a logical name; the Kafka topic and the connector are in `application.properties` (`mp.messaging.<dir>.<channel>.topic` defaults to the channel name, `.connector = smallrye-kafka` vs in-memory). Round-1 Quarkus defers messaging → notification-service's `notification` producer + `input` consumer, and all of event-statistics, are invisible. | round-2-java-quarkus / `MossabTN/quarkus-microservices-poc` @ `4da7005` (notification-service) + `quarkus-super-heroes` event-statistics | New reactive-messaging detector: `@Incoming(ch)`/`@Outgoing(ch)` on a method + `@Channel(ch)` on an injected `Emitter`/`MutinyEmitter`; resolve `ch` (often a constant) via the evaluator; map channel→topic through `mp.messaging.<dir>.<ch>.topic` (default = channel name); gate the protocol=kafka edge on `.connector` being a Kafka connector (in-memory channels must NOT become Kafka edges). Payload = the method's message/emitter generic. Needs a small properties read (targeted, or the promoted neutral config layer of #43). | high | ✅ implemented |
| 45 | **Quarkus programmatic `RestClientBuilder` client not detected** — besides `@RegisterRestClient`, MicroProfile clients can be built imperatively: `RestClientBuilder.newBuilder().baseUri(uri).build(VillainClient.class)`. rest-fights' villain client is built this way → the fights→villains edge is missed while heroes/narration (annotation-based) resolve. | round-2-java-quarkus / `quarkus-super-heroes` rest-fights (`VillainClient`/`VillainService`) | Detect `RestClientBuilder.newBuilder()....build(<Iface>.class)`: the `.build(X.class)` target names the client interface (raw logical target, like the bare-`@RegisterRestClient` case) and `.baseUri(<expr>)` in the chain resolves the URL via the value+config layer. Same edge shape as the annotation client. | medium | |
| 46 | **Quarkus OpenAPI-generated JAX-RS resources** — like Spring #1 but for Quarkus: with `quarkus-openapi-generator-server`, the `@Path`/`@GET` server interface (`HeroesResource`, `FightsResource`) is generated at build time from `src/main/resources/openapi.yml` and the resource class only `implements` it → those endpoints are not in source. rest-heroes/rest-villains/rest-fights find only the hand-written resources (e.g. `UIResource` `GET /`). | round-2-java-quarkus / `quarkus-super-heroes` (rest-heroes, rest-villains, rest-fights) | Wire the existing OpenAPI `SpecIngester` (#1) into the Quarkus provider: when a `@Path` resource implements an interface absent from source AND an `openapi.*` spec exists, ingest endpoints from the spec (dedup against source). The spec reader is framework-neutral; only the provider hook is new. | medium | |
| 47 | **Kotlin services need a language layer (Recipe B)** — closes the language half of #42. The Java providers glob `**/*.java`, so any Kotlin JVM service (Spring/Micronaut/Quarkus in Kotlin — a large and growing share) is invisible. | round-1-java-micronaut documents-service (#42) + general | Built `internal/provider/lang/kotlin` (tree-sitter-kotlin `File`/`Node`/`QueryRunner`/parser + annotation helpers) and a first framework provider `internal/provider/springkt` (Spring Boot Kotlin REST). tree-sitter-kotlin node shapes differ from Java (function_declaration, simple_identifier, annotations as user_type/constructor_invocation, string_literal>string_content) — the kotlin annotation helpers hide that behind the same API as lang/java. Round 1 = REST endpoints; the Kotlin evaluator, type/schema source, and client/kafka detectors are the next rounds. | high | ✅ implemented (REST) |
| 48 | **Node.js needs a TS/JS language layer (Recipe B)** — the first non-JVM stack. NestJS is decorator-based (`@Controller('cats')` + `@Get(':id')`) and maps cleanly to the annotation model, but over tree-sitter-typescript where decorators are PRECEDING SIBLINGS of the class/method (or children of a non-exported class_declaration), strings are `string>string_fragment`, and `@Controller({ path: 'auth' })` uses an object-options form. | round-4-node-nestjs / `Brocoders/nestjs-boilerplate` + `Denrox/nestjs-microservices-example` | Built `internal/provider/lang/tsjs` (TypeScript grammar, superset that also parses backend JS) + `internal/provider/nestjs` REST detector: `@Controller` base (positional string OR `{path}` object) + `@Get/@Post/...` method decorators, `:id`→`{id}` path-var normalization for cross-language graph uniformity. Round 1 = REST; `@MessagePattern`/`@EventPattern` microservice edges, HttpService/axios clients, and DTO schemas are next rounds; Express (call-based routing) is a sibling provider on the same layer. | high | ✅ implemented (NestJS REST) |
| | 43 | **Micronaut config placeholder resolution** — `@Client("${elastichealth.endpoint}")`, `@Topic("${...}")` and `@Controller("${...}")` stay honest-uncertain because round-1 Micronaut ships without a config resolver (`Index.Config` nil). Micronaut uses the SAME `application.yml`/`.properties` + `${}` model as Spring, so the Spring config/deploy layer is directly reusable. | round-1-java-micronaut / `asc-lab/micronaut-microservices-poc` @ `9871a2e` (dashboard-service ElasticHealthCheck) | Promote Spring's `configIndexer`/`deployIndexer`/`springConfig` resolver out of `provider/spring` into a neutral JVM-config package (GUIDELINE §2.4) and wire it into the Micronaut provider's `Indexers()`; the `${}` resolver, relaxed binding and deploy layer are all framework-free. | medium | |

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

## Round 10 result (2026-07-21) — two new repos, reactive + interface-contract stressors; scans only, fixes pending
Targets (9 services, all exit 0), bench artifacts in `service-discovery-repos/round-10/_bench/`:
`piomin/sample-spring-microservices-new` @ `e6437b4` (Feign+Eureka+Gateway mesh, tier-2) and
`PacktPublishing/Microservices-with-Spring-Boot-and-Spring-Cloud-Third-Edition` @ `e4820dd`
(SB3.2 **Chapter15** — WebFlux + Spring Cloud Stream + Config Server + Gateway, tier-5).
What VALIDATED on new real code: piomin is a clean **positive control** — endpoints
5/5 · 5/5 · 6/6 and Feign 0/1/2 all 100%/100%, and client-side `@GetMapping` on the
`@FeignClient` interfaces correctly NOT emitted as the service's own endpoints; #14
Cloud Stream functional-consumer detection fires on the magnus book code once config is
present; #16 `--config-repo` + #29 Gradle shared-module discovery both exercised end to end.
New findings, all from the magnus reactive stack:
#33 (interface-hosted REST mappings — the `@RestController` implements an `:api`-module
interface that owns the annotations → **0 endpoints** on all 4 core services),
#34 (`StreamBridge.send` producer invisible → composite's 3 producer edges missed),
#35 (config-repo not scoped by `spring.application.name` → recommendation/review emit the
WRONG consumer topic `products` — a correctness bug, not just a miss),
#36 (WebClient `.uri(URI)` via `UriComponentsBuilder.fromUriString(CONST+path)` → composite's
3 outbound edges collapse to one anonymous uncertain).
Also re-seen: Spring Cloud **Gateway static routes** (`spring.cloud.gateway.routes[].uri:
lb://…` in magnus `config-repo/gateway.yml` → `product-composite`, `auth-server`) still not
parsed (the round-8 open question). Low urgency here: in magnus the same targets are already
covered by coded `HealthCheckConfiguration` WebClient calls (`/actuator/health`, found 5/5),
and piomin's gateway uses the discovery-locator (no static route edges — 0 is correct). Scope
decision still open.

## Round 11 result (2026-07-22) — Kafka + Eureka mesh, run against the LIVE backend
Target: `omkarnikam24/springboot-microservices-kafka` @ `2d9e4b1` (3 modules: auth-service,
profile-service, eureka-service). Ran the full authenticated pipeline against a live `ekgd`
(`--api-url` + key): validate → ingest → per-commit diff, artifacts in `round-11/_bench/`.
Clean recall on everything we model: auth-service endpoints 3/3 (`POST|PUT|DELETE
/assignement/profile`) + Kafka producer `profile-topic` `likely` (resolved via
`@Value("${com.amdocs.kafka.topic-name}")`); profile-service endpoint 1/1 (`POST /profile`) +
consumer `profile-topic` `confirmed` (`@KafkaListener(topics=TOPIC_NAME)` literal). The
producer and consumer connect on `profile-topic`, both carrying schema `ProfileDTO` — the
async graph closes. eureka-service = 0 edges (negative control, correct).
**Live backend verified end to end**: three services ingested → three baselines stored
(`auth-service.json`, `profile-service.json`, `eureka-service.json`); first submit returned the
PR-comment diff (auth 5 added / profile 2 added / eureka none); a re-submit of auth-service
returned **zero churn** (no comment) — byte-stable determinism holds against the live diff
engine. New finding #37 (`EurekaClient.getApplication(@Value applicationName)` discovery target
— the only miss: auth→profile emits `http://{?}:{?}/profile` uncertain instead of the logical
name `PROFILE-SERVICE`). Note: auth-service's `service_id` is `auth-service` (dir) though its
`spring.application.name` is `auth-server` — worth confirming which the backend should key on,
related to the round-9 (repository, service_id) baseline note.

## Round 11 fix result (2026-07-22) — #37 implemented (Option A), verified on the live backend
Implemented in the outbound-target emit path (`spring/discovery.go` + `emitTargets` in
`deps.go`): when a RestTemplate/WebClient URL resolves to no host (Unknown, or a Template whose
host is a runtime hole), we look in the SAME method for a registry lookup
(`EurekaClient.getApplication(<arg>)` / `getNextServerFromEureka(<arg>)`, the former gated on a
`com.netflix.discovery` import so the generic name can't false-positive), resolve `<arg>`
through the value+config layer, and emit that logical name as the target — replacing (not
duplicating) the anonymous uncertain edge. Confidence `likely` (registry indirection); arg
unresolvable → falls through to today's honest uncertain. Tests: `TestRestTemplateEurekaDiscovery
Target` (positive, `@Value`→config→`PROFILE-SERVICE`) + `TestRestTemplateGetApplicationUngated
WithoutEureka` (negative, no import → stays anonymous); full suite green, zero regressions.
Live re-submit to the backend: auth-service diffed **1 added · 1 removed** — `PROFILE-SERVICE`
(resttemplate, likely) replaces `http://{?}:{?}/profile` (uncertain); profile/eureka zero churn.
The backend mapped `PROFILE-SERVICE` → the `profile-service` node, so the panel graph now shows
the `unknown-target` node gone and TWO resolved `auth-service → profile-service` edges (REST
likely + Kafka confirmed) — the sync and async paths both land on the real service.
Deferred (Option B, if we want the path back): keep the `/profile` shape on the edge by tracking
that an `InstanceInfo` from `getApplication(X)` carries `X` as its host identity.

## Round 12 result (2026-07-22) — gRPC + reactive-Kafka repo; near-zero coverage, three findings; scans only
Target: `galleog/piggymetrics-k8s` @ `ff2128a` (a full rewrite of piggymetrics — Gradle, WebFlux
functional, gRPC for sync, reactor-kafka for async, Helm/k8s). 4 Spring services scanned, all exit 0;
artifacts in `round-12/_bench/`. This repo is a deliberate stressor and it stressed hard — the
extractor sees almost nothing here, which is itself the signal.
What the tool got: api-gateway's functional routes fire the #20 detector — but WRONG (see #38):
7 real endpoints (`/accounts/*`, `/notifications/*`, `/statistics/*`) collapse to 3 prefix-stripped,
deduped paths → 0/7 recall + 3 false positives. Everything else is empty.
New findings: #38 (RouterFunction `.path(prefix,…)` prefix not composed + dedup collapse — a
correctness bug, emits wrong paths), #39 (reactive Kafka / reactor-kafka `ReactiveKafka*Template`
producers + consumers — the entire account/statistics/notification event choreography invisible),
#40 (the gRPC sync mesh — deferred milestone, but galleog is a strong prioritization argument:
`@GrpcService`/`@GrpcClient` + `.proto` defs all present and Feign-shaped).
Live backend: all 4 submitted (exit 0) under `github.com/galleog/piggymetrics-k8s`, but they don't
surface in the panel — galleog's default branch is `master` while the extractor has no
`--default-branch` flag, so the backend defaults to `main`; branch `master` ≠ default `main` is
treated as a non-default (feature) branch → every scan reports "(first scan)", no `main` baseline is
written, and the panel's main-branch graph stays empty. (Also the graph would be 4 disconnected
nodes — every edge is a #39/#40 miss.) Minor CLI/backend gap worth a `--default-branch` flag
(or auto-detecting the repo's default branch), separate from the detection findings above.

## Micronaut round 1-2 (2026-07-24) — NEW FRAMEWORK: Micronaut (JVM, Recipe A); benchmark-driven, 100% in-scope
First non-Spring provider. `internal/provider/micronaut/` reuses the `lang/java` layer verbatim
(parser, value evaluator, symbol/type/schema indexers) and adds three detectors:
`micronaut.rest` (@Controller + @Get/@Post/@Put/@Delete/@Patch/... path composition),
`micronaut.client` (@Client declarative HTTP client — polymorphic value: service-id / URL / path /
${placeholder}, or explicit `id=`), `micronaut.kafka` (@KafkaClient producer + @KafkaListener
consumer, both via method/param @Topic; payload = the non-metadata body parameter). New
`model.DetectMicronautClient`. Match keys on `io.micronaut` in build files + `import io.micronaut`
(scores 0 on Spring, beats Spring's bare-pom score on a Micronaut repo). Registered as the second
provider; detection stays fail-loud on ties.

Benchmark: `asc-lab/micronaut-microservices-poc` @ `9871a2e` (497★, real insurance-domain
microservices), 4 Java services hand-labeled in `round-1-java-micronaut/_bench/`:

| service | endpoints | outbound | producers | consumers |
|---|---|---|---|---|
| policy-service | 6/6 | 1/1 (pricing-service) | 2/2 | — |
| dashboard-service | 3/3 | 0 (+1 honest-uncertain elastic ${}) | — | 1/1 |
| agent-portal-gateway | 12/12 | 6/6 | — | — |
| policy-search-service | 1/1 | — | — | 2/2 |

**100% precision AND recall on every in-scope category** (22 endpoints, 7 clients, 2 producers,
3 consumers). Round 1→2 finding: **#41** — the API-interface controller pattern (bare `@Override`
methods, mappings on a sibling-module interface) missed 4/6 endpoints in policy-service; fixed with
the neutral `Index.HTTPContracts` + contract indexer (also unblocks Spring #33). Gaps recorded as
labels: **#42** Kotlin `documents-service` (out of scope — needs a Kotlin layer), **#43** the
`${elastichealth.endpoint}` client placeholder (honest-uncertain until the Spring config layer is
promoted to a neutral package and wired in).

Regression: baseline binary (`8fc899c`, pre-Micronaut) vs current produce **byte-identical** output
on all 12 Spring roots the harness flagged (the flags/code-drift differences vs the committed
snapshots are not mine) — zero regressions across tier-1…5 + round-10…12. Full gate green
(build/vet/gofmt/test ./...); real-engine detector tests for all three detectors.

Micronaut round 3 (2026-07-24) — second repo, Kafka-heavy: `piomin/sample-kafka-micronaut-microservices`
@ `ff75f5b` (4 Java services, a taxi-domain event mesh; `round-1-java-micronaut/_bench_piomin/`).
**100% precision AND recall, no new findings**: all 12 in-scope Kafka edges (5 `@KafkaClient`
producers, 7 `@KafkaListener` consumers, across `orders`/`trips`/`drivers`) + order-service's 2
endpoints (`@Controller("orders")` with no leading slash composed to `/orders`). Validates the Kafka
detector on the producer-interface (`@KafkaClient` + method `@Topic`) form end-to-end. Micronaut now
100% across **8 services in two real repos**; remaining gaps are the documented #42/#43.

Micronaut round 4 (2026-07-24) — third repo, HTTP-client mesh: `piomin/sample-micronaut-microservices`
@ `8547313` (department/employee/organization services, Consul discovery;
`round-1-java-micronaut/_bench_piomin_http/`). **100% precision AND recall, no new findings**: 16
endpoints (multi-`@Get` controllers, path variables, deep sub-paths like
`/organizations/{id}/with-departments-and-employees`) + 3 declarative-client edges
(`@Client(id="…", path="…")` → employee/department targets). Micronaut final tally: **100% across 11
services in three real repos** (asc-lab + two piomin), covering REST (incl. the API-interface
pattern), `@Client` in all four value forms, and both Kafka directions. Provider considered done for
the in-scope surface; open items are the deferred #42 (Kotlin) and #43 (config placeholders).

## Quarkus round 1-2 (2026-07-24) — NEW FRAMEWORK: Quarkus (JVM/JAX-RS, Recipe A); benchmark-driven, 100% in-scope
Third provider. `internal/provider/quarkus/` reuses the `lang/java` layer + the neutral
`HTTPContracts` machinery, and adds two detectors for the JAX-RS model (where the HTTP verb and the
path are SEPARATE annotations, unlike Spring/Micronaut): `quarkus.rest` (@Path resource class +
@GET/@POST/... methods, method @Path composition, JAX-RS body-param detection, `Uni`/`Multi`/
`RestResponse` return unwrapping, and the API-interface pattern via HTTPContracts) and
`quarkus.restclient` (@RegisterRestClient — configKey / baseUri / bare-interface-name targets). New
`model.DetectMPRestClient`. Match keys on `io.quarkus` (JAX-RS alone is not enough — other stacks use
it). Reactive-messaging deferred (#44).

Benchmarks (`round-2-java-quarkus/_bench/`):

| repo / service | endpoints | outbound (rest-client) |
|---|---|---|
| MossabTN/quarkus-microservices-poc `4da7005` / product-service | 12/12 | 2/2 (keycloak-token, users) |
| … / customer-service | 7/7 | 2/2 (keycloak, keycloak-token) |
| … / order-service | 5/5 (PUT is commented-out — correctly excluded) | 3/3 (customer, product, keycloak-token) |
| … / notification-service | 6/6 | 2/2 (+ #44 messaging gap) |
| quarkusio/quarkus-super-heroes / rest-fights | — (OpenAPI-generated, #46) | 2/2 (hero-client, narration-client; +#45 villain) |

**100% precision AND recall on every in-scope category** — 30 endpoints + 11 `@RegisterRestClient`
edges, zero wrong, zero missed, no fixes needed (the JAX-RS detector was right on the first
benchmark). Correctly handles commented-out code (tree-sitter drops it), the interface-vs-class
distinction (client interfaces with a class-level `@Path` are NOT emitted as server endpoints), and
verb+separate-`@Path` composition. New findings are all documented gaps, not misses: #44
(reactive-messaging), #45 (programmatic `RestClientBuilder`), #46 (OpenAPI-generated resources —
Spring #1 for Quarkus). Full gate green; real-engine detector tests for both detectors incl. the
interface pattern. Regression: registry now has 3 providers; Spring/Micronaut output unchanged
(Quarkus scores 0 on non-Quarkus repos — `io.quarkus` required).

## Quarkus round 3 (2026-07-24) — reactive messaging (#44 implemented); config-aware, 100%
Added `quarkus.messaging` (MicroProfile Reactive Messaging: `@Incoming`/`@Outgoing` methods +
`@Channel` Emitter injection) and a lightweight `configIndexer` that parses application.properties/
yaml into a flat `Index.Config` (single-layer, `${x:default}` expansion, profile-scoped `%dev.`/
`%test.` keys dropped). The detector resolves the channel name (literal OR a `static final`
constant, via the evaluator), maps channel→topic through `mp.messaging.<dir>.<ch>.topic` (default =
channel name), and emits a Kafka edge ONLY when the channel's `.connector` is a Kafka connector — so
SmallRye in-memory channels never become false topics.

Bench (`round-2-java-quarkus/_bench/`): **100% on both messaging services**, and a real new edge on
rest-fights that connects the Superheroes mesh:

| service | producers | consumers | note |
|---|---|---|---|
| MossabTN notification-service | 1/1 (`notification`) | 1/1 (`notification`) | `@Incoming("input")` → topic `notification` via config — WRONG without config resolution |
| super-heroes event-statistics | — | 1/1 (`fights`) | `@Incoming(FIGHTS_CHANNEL_NAME)` constant resolved; in-memory `winner-stats`/`team-stats` correctly skipped |
| super-heroes rest-fights | 1/1 (`fights`) | — | `@Channel("fights") MutinyEmitter` → rest-fights now connects to event-statistics |

Key validations: config-driven channel→topic (the `input`→`notification` remap would be wrong
otherwise), channel-name constant folding, Kafka-vs-in-memory connector gating, and `%profile.` key
exclusion. Found-while-implementing: a positional constant annotation arg (`@Incoming(CONST)`) reached
neither the named-value path nor the literal path — fixed `channelName` to grab the positional node
and fold it. No regressions on the other Quarkus services (product/customer/order still 100%, no
messaging false positives). Full gate green; unit tests cover channel→topic remap, in-memory gating,
and constant channels. Quarkus final: **100% across 7 services in two repos** (REST + rest-client +
messaging); open gaps are #45 (programmatic RestClientBuilder) and #46 (OpenAPI-generated resources).

## Kotlin round 1 (2026-07-24) — NEW LANGUAGE: Kotlin (Recipe B); Spring-Kotlin REST, 100%
First non-Java language layer. `internal/provider/lang/kotlin/` mirrors `lang/java`'s Node API but
over the tree-sitter-kotlin grammar (different node types: `function_declaration`,
`simple_identifier`, `class_body`, annotations as `user_type`/`constructor_invocation`,
`string_literal > string_content`); its annotation helpers give detectors the same surface as the
Java layer. First provider on top: `internal/provider/springkt` (`spring-boot-kotlin`) — a REST
detector for `@RestController`/`@Controller` classes with `@GetMapping`/`@PostMapping`/... path
composition, path variables, and the classic `@Controller`+`@ResponseBody` style. Match requires
`.kt` sources (score 0 otherwise, so pure-Java repos stay with the Java provider) and beats the Java
provider's build-file score on a Kotlin repo via a `.kt` `@SpringBootApplication`.

Benchmark (`round-3-kotlin-spring/_bench/`): **100% precision AND recall** on 7 endpoints across two
real repos — `sdeleuze/spring-boot-kotlin-demo` (638★, by a Spring-framework committer;
CustomerController → `GET /customers`, `GET /customers/{lastName}`) and
`callicoder/kotlin-spring-boot-jpa-rest-api-demo` (ArticleController `@RequestMapping("/api")` CRUD →
5 endpoints incl. `PUT`/`DELETE /api/articles/{id}`). Detection correctly emits `language: "Kotlin"`
and picks `spring-boot-kotlin` over the Java provider. No regressions: the Java Spring/Micronaut/
Quarkus providers are byte-identical (springkt scores 0 without `.kt`); petclinic still `language:
Java` with its one REST endpoint. Full gate green; real-engine detector tests over the Kotlin parser.
Round 1 scope is endpoints; the Kotlin value evaluator, DTO/schema source, and client/Kafka detectors
(and Kotlin-Micronaut/Quarkus providers, which would reuse this layer) are the next rounds — this
round proves the language layer end-to-end.

### Quarkus messaging — full-mesh validation (correction to round 3)
A final full-benchmark sweep found the messaging detector also (correctly) emits the Kafka edges in
the OTHER MossabTN services, which round 3's spot-check hadn't labeled: customer-service produces
`notification` (`@Channel("notification")`), order-service produces `product` (`@Channel("product")`),
product-service consumes `product` (`@Incoming("input")` → `mp.messaging.incoming.input.topic=product`).
Labels updated; all resolve via config at `smallrye-kafka`. The four services form a consistent event
mesh (customer→notification→notification-svc; order→product→product-svc) — end-to-end evidence the
channel→topic config resolution and connector gating are correct across a whole repo, not one service.
Final tally: **all 19 new-provider benchmark services score zero-missed / zero-wrong** on every
category with truth (the only non-perfect cells are documented honest-uncertain/gap items — the
elastichealth `${}` client and the programmatic-RestClientBuilder villain client).

## Node.js / NestJS round 1 (2026-07-25) — NEW STACK: Node.js (TypeScript, Recipe B); NestJS REST, 100%
First non-JVM language. `internal/provider/lang/tsjs` mirrors the Node API over tree-sitter-typescript
(decorators as preceding siblings / class children, `string>string_fragment`, object-options args).
`internal/provider/nestjs` (`nestjs-typescript`) adds a `@Controller` + `@Get/@Post/...` REST detector
with base-path composition (positional `@Controller('cats')` AND object `@Controller({path})` forms)
and `:id`→`{id}` normalization so a Spring caller's `/cats/{id}` matches a NestJS `/cats/:id`.

Benchmark (`round-4-node-nestjs/_bench/`): **100% precision AND recall** on 31 endpoints across two
real repos — `Brocoders/nestjs-boilerplate` (9 controllers → 22 distinct endpoints; three identical
`POST /files/upload` uploader impls correctly dedupe to one) and `Denrox/nestjs-microservices-example`
gateway (9 endpoints, predicted BEFORE running and matched exactly). Detection emits `language:
"TypeScript"` and picks NestJS (score 0 without `.ts`, so JVM repos are unaffected — verified
petclinic/Kotlin/Quarkus still detect correctly). The backend services' `@MessagePattern` handlers
(microservice transport = the async mesh) are a documented round-2 gap. Full gate green; real-engine
detector tests over the TS parser.

## Node.js / Express round 1 (2026-07-25) — Express route detection (call-based), relative paths 100%
`internal/provider/express` (`express-node`) on the shared lang/tsjs layer — the call-based sibling of
NestJS. Handles `app.get('/p', h)`, `router.post('/p', h)`, and the chained `router.route('/p').get(h)
.post(h)` form; rejects the `.get`/`.post` look-alikes (settings getter `app.get('port')`, `cache.get(
'key', def)`) via the "path starts with /" filter + receiver check. Match defers to NestJS. Benchmark
`round-5-node-express/_bench/`: **100% on the 9 distinct route DECLARATIONS** of
`maitraysuthar/rest-api-nodejs-mongodb` (10 routes, book.js+index.js `GET /` dedupe) — predicted from
the route files before running. The nested cross-file `app.use` mount prefix (#50) is the documented
gap: paths are module-relative, not the full `/api/auth/...`. No regressions (Match needs an express
dep + no @nestjs). Node.js now covers the two dominant frameworks (NestJS decorators + Express calls).
