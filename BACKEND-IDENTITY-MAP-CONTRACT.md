# Backend contract reconciliation — identity-map ingest & join

**To:** backend agent
**From:** extractor side, after an end-to-end test against the live backend (`localhost:3000`, key `ekg_dev_local_demokey`)
**Status:** the Service-graph loop works; the **identity-map half is on an older contract than the extractor now emits, and the join is not observable.** Three issues below, with evidence and the target contract.

---

## TL;DR

1. **`environment: ""` is rejected** — the extractor legitimately emits an empty environment for entries with no overlay (all `k8s-raw`, `terraform`, Helm-base). Backend must accept it.
2. **`hosts` shape mismatch** — backend expects `hosts: ["str", …]`; the extractor now emits `hosts: [{value, kind, resolver}]`. The `kind` field is load-bearing for the join (see #3). Backend must adopt the object shape.
3. **The join is not wired / not observable** — after storing 22 identities, a caller whose host matches a stored identity resolves identically to one that matches nothing (`external` in both cases). The join must (a) key on the caller's resolved **host** (from `url`), not `target_name`, and (b) be observable in a response.

Root cause: version skew. The backend implements the contract from the *first* deploy-repo handoff (hosts as `[]string`). The resolver-framework change (per-host provenance, two-class matching) landed on the extractor after, and was not applied backend-side.

---

## Test setup (to reproduce)

- Callee identity: `extractor --mode deploy-repo --root <argocd-example-apps> --dry-run` → 22 entries incl. `guestbook-ui` with in-cluster hosts.
- Caller: a Spring service with `@FeignClient(name="guestbook", url="${guestbook.url}")` + `application.yml: guestbook.url: http://guestbook-ui`. Produces one outbound dependency:
  ```json
  { "target_name": "guestbook", "url": "http://guestbook-ui", "detection": "feign", "resolved": true }
  ```
  Note: the **host `guestbook-ui` is in `url`; `target_name` is the Feign logical name `guestbook`.** They diverge — which is the entire reason this feature exists.

---

## Issue 1 — `environment` must allow empty string

**Evidence** — submitting the current identity map:
```
HTTP 400  {"message":"entries[0].environment must be a non-empty string","error":"Bad Request"}
```

**Why the extractor emits `""`:** `environment` is the overlay/values file an entry was rendered from (e.g. `"staging"` from `values-staging.yaml`, or a Kustomize overlay dir name). Entries with no overlay — every raw manifest, every Terraform module, a Helm chart's base render — have **no environment**, and `""` is the correct, meaningful value ("not applicable / base"). It is part of the entry's identity key, so it must round-trip.

**Fix:** accept `environment: ""` (treat empty as the base/unspecified environment). Do not require non-empty.

---

## Issue 2 — `hosts` is an array of objects, not strings

**Evidence** — after fixing environment locally, the next validation error:
```
HTTP 400  {"message":"entries[0].hosts[0] must be a non-empty string","error":"Bad Request"}
```
Only when I flattened `hosts` to `["guestbook-ui", …]` and set non-empty environments did it store: `HTTP 200 {"repository":…,"stored":22,"added":22}`.

**Current extractor output (the target contract):**
```json
{
  "repository": "github.com/argoproj/argocd-example-apps",
  "entries": [
    {
      "service_name": "guestbook-ui",
      "hosts": [
        { "value": "guestbook-ui",                            "kind": "in-cluster", "resolver": "k8s-raw" },
        { "value": "guestbook-ui.default",                    "kind": "in-cluster", "resolver": "k8s-raw" },
        { "value": "guestbook-ui.default.svc",                "kind": "in-cluster", "resolver": "k8s-raw" },
        { "value": "guestbook-ui.default.svc.cluster.local",  "kind": "in-cluster", "resolver": "k8s-raw" }
      ],
      "namespace": "default",
      "environment": "",
      "source": "k8s-raw",
      "confidence": "confirmed"
    }
  ]
}
```
- `kind` ∈ `"in-cluster" | "external" | ""`. **This drives the join** (Issue 3) — an in-cluster host matches by bare-name normalization; an external host matches exactly. Dropping `hosts` to `[]string` discards this and makes a correct join impossible.
- `resolver` ∈ `helm | kustomize | k8s-raw | self-declared | terraform` — provenance; useful for trust arbitration, not required for the match.
- `kind`/`resolver` are `omitempty`; a `self-declared` host may have no `kind`.

**Fix:** parse `hosts` as an array of `{value, kind?, resolver?}` objects; `value` is the string to join on.

---

## Issue 3 — the join must key on the resolved host, and be observable

**Evidence** — with 22 identities stored (old shape), submitting callers:

| Caller | `target_resolutions` |
|---|---|
| `target_name=guestbook`, `url=http://guestbook-ui` (host **is** stored) | `{"guestbook\|feign": "external"}` |
| `target_name=guestbook-ui` (name **is** a stored host) | `{"guestbook-ui\|feign": "external"}` |
| `target_name=totally-unknown-zzz` (nothing stored) — control | `{"totally-unknown-zzz\|feign": "external"}` |

All `external` — **matched and unmatched are indistinguishable.** `target_resolutions` is keyed by `target_name|detection` and does not consult the identity map. There is also **no GET/query endpoint** (only `POST /v1/auth/validate`, `/v1/ingest`, `/v1/ingest/identity-map`) to observe a completed edge.

**Required join behavior:**
1. For each caller outbound dependency, derive candidate host(s) from the **`url`** field (parse out the host; `http://guestbook-ui` → `guestbook-ui`), not only `target_name`. `target_name` is often a logical/Feign name that differs from the deployed host — matching on it structurally cannot work.
2. Match candidates against stored identity `hosts[].value`, using `kind`:
   - **`in-cluster`** → normalize the caller's host to its bare service name (strip `:port`, and a trailing `.<ns>.svc.cluster.local` / `.<ns>.svc` / `.<ns>`), then compare bare-to-bare. So a caller resolving `guestbook-ui`, `guestbook-ui.default`, or `guestbook-ui.default.svc.cluster.local` all match the `guestbook-ui` identity.
   - **`external`** → exact string match (lowercased, scheme/port stripped).
   - **empty kind** (self-declared) → exact match; may also try bare-name.
3. On match, the edge resolves to the identity's `service_name` (then onward to a canonical service via the existing name→service_id mapping). Surface this — either flip the edge's `target_resolutions` value from `external` to the resolved service (or an `internal`/`resolved_service` field), or expose a query endpoint returning completed/inbound edges.

**Ownership nuance (unchanged, still open):** a host match yields a `service_name`, but `helm`/`kustomize`/`k8s-raw`/`terraform` identities carry no link to an onboarded code repo (`repository`). Only `self-declared` entries carry that link (shared `repository`). Deciding whether a host-matched-but-unlinked target counts as "resolved" vs "external, host-known" is a product/backend call — but it must at least be *distinguishable* from a target that matched nothing, which today it is not.

---

## Action checklist (backend)

- [ ] Accept `environment: ""` on identity-map entries.
- [ ] Parse `hosts` as `[{value, kind?, resolver?}]`; join on `value`, branch on `kind`.
- [ ] Join caller dependencies on the **`url` host** (normalized), not `target_name`.
- [ ] Implement the two-class match (in-cluster bare-name-normalize / external exact).
- [ ] Make the result observable (ingest-response field or a query endpoint); today matched vs unmatched are identical (`external`).

## Extractor side (what we will / won't change)

- The extractor stays on the `[{value, kind, resolver}]` + `environment:""` contract — it carries the information the two-class join needs.
- If the backend cannot adopt the object shape immediately, we can add a temporary `--legacy-hosts` flag emitting `hosts: []string` + a non-empty environment sentinel, **but that discards `kind`**, degrading the join to exact-match-only. Prefer the object shape.
