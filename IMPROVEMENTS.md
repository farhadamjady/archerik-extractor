# Improvements found during benchmark testing

Findings from testing against real open-source repos. We implement these
AFTER the testing phase is done. Each entry says where it was found and what
to do.

| # | Finding | Found in | What to do | Priority |
|---|---------|----------|------------|----------|
| 1 | Controllers that implement **OpenAPI-generated interfaces**: the mapping annotations (`@GetMapping` etc.) are not in the source — they are generated at build time from `src/main/resources/openapi.yml`. We find ~1 of ~40 endpoints there. | tier-1 / `spring-petclinic-rest` @ `c7b5f5e` | Add an OpenAPI reader: when a `@RestController` implements an interface we cannot find in the source AND an `openapi.yml`/`openapi.json` exists, read endpoints (+ schemas) from that file. This partly reverts the "OpenAPI cut from MVP" decision — the pattern is real and common. | high |

## How to add a new entry
When a benchmark tier finds a miss: add a row here with the repo + commit SHA,
and if possible add a small unit-test fixture in the extractor that reproduces
the miss (so the fix later has a failing test ready).
