# Module Distribution v2 — Decentralized, Helm-style

Status: draft · Supersedes: the `api.tinysystems.io` catalog as source of truth

## 1. Why change

Today `tiny` resolves modules through `internal/catalog` → `api.tinysystems.io`,
which is the **source of truth**: it mints short ids (`http-module-v0`), hosts
images on a private registry (`europe-west2-docker.pkg.dev/tinyplatform/…`), and
gates discovery. That is opaque (an id doesn't map to a visible source), fragile
(if the site dies, install dies), and reads as shady to developers who audit
what they run on their own cluster.

**Goal:** the module **repo + its published image are the source of truth**;
the platform becomes an optional helper (index, search, curation, mirror) that
**builds its index from repos**, never the other way round. `tiny` must fully
work with no central service alive — you are giving the technology away.

This is the Helm model: a repo is static files, clients need no server, and the
proxy is a cache, not a gate (cf. Go modules + `GOPROXY=direct`).

## 2. Principles

1. **Source of truth = the module repo + its signed image.** Not a database.
2. **Identity = the repo coordinate** (`github.com/org/repo`), transparent by
   construction. No minted slugs.
3. **Platform = aggregator / mirror / curation, never a gate.** Truth flows
   repos → index. `api.tinysystems.io` is just another repo URL.
4. **Helm-native, convention over machinery.** Prefer deleting interpretation
   layers over adding config.
5. **Nothing that says "trust me."** Everything a dev audits — id, image,
   values, RBAC, egress — is inspectable, minimal, and signed.
6. **No account to use your own cluster.** Central calls are optional and
   bypassable; offline install works from cache.

## 3. Concepts

### 3.1 Repo (index)
A **repo is static files**: an `index.yaml` served from any durable location —
GitHub Pages, a GHCR OCI artifact, an object bucket, a raw repo file. No server.
Mirrors trivially. `tiny` keeps a client-side list of repos (Helm-style) and a
local merged, cached index.

- A **default repo** is baked into `tiny` (the tinysystems index, hosted as
  static files), removable.
- Users `tiny repo add <name> <url>` more.

### 3.2 Module
A module is **not a chart**. It is:

- an **image** (`ghcr.io/org/repo:version`, or any registry — explicit ref),
- a **values overlay** for the shared harness chart,
- optional **bundles** (see 3.5),
- **display metadata** (description, category) for discovery.

Identity = the repo coordinate. Version = SemVer (§7).

### 3.3 Harness chart
The shared `tinysystems-operator` chart is a generic "run this image as a
controller-manager" wrapper. One chart, **N releases** (one per module), N
images — this is already true today (`provision.InstallModule` installs the
operator chart per module with `controllerManager.manager.image.*`).

- The harness chart lives in the **default repo** like any other artifact —
  nothing platform-special.
- Its version is pinned globally today (`operatorVersion = 0.2.10`). v2 moves
  that into **per-module metadata** so a module declares the harness range it
  needs (`chartVersion: ">=0.2.0 <0.3.0"`), avoiding silent breakage.

### 3.4 Values overlay + cluster-filled holes
Module install config collapses to **Helm values**. The module ships a
`values.yaml` (image ref, `rbac.enableKubernetesResourceAccess`,
`ingress.enabled`, …). This **deletes** the current `requires_ingress →
interpret → --set` layer in `provision.moduleValues`.

The only non-static values are the ones the module can't know because they are
the **cluster's** — model them as named holes `tiny` fills from `tiny up`
settings:

```yaml
# module values.yaml (shipped in the repo / image)
controllerManager:
  manager:
    image: ghcr.io/tiny-systems/http-module:1.4.2
rbac:
  enableKubernetesResourceAccess: true
ingress:
  enabled: true
  className: "${cluster.ingressClass}"      # tiny fills from cluster settings
```

Install = merge(module values, cluster fills) → `helm upgrade --install`.

Known cluster fills: `cluster.ingressClass`, `cluster.storageClass`,
`cluster.brokerURL` (extendable).

### 3.5 Bundles
A bundle is **the exception to "just values"**: per the SDK
(`module/requirements.go`), it is *an additional Helm release per bundle* —
third-party charts (pgvector, a vector DB, an embedding endpoint). So a module
install is:

```
1 harness release   (module image + values)      ← just values
+ N bundle releases  (each its own chart + values) ← separate helm installs
```

Still plain Helm, one level out. A bundle entry = `{ chart ref, version, values,
defaultOn }`; the chart ref is `oci://…` or a Helm repo URL. Semantics
(unchanged from today): omit → module defaults; `["none"]` → zero; explicit
list → exactly those. **Never force a bundle** (`["none"]` must always work).

> Gap: `tiny`'s local installer currently warns-and-ignores bundles
> (`internal/installer`). v2 must resolve + `helm upgrade --install` each
> selected bundle.

### 3.6 Metadata home
Store metadata **at the module** (source of truth); the index is a generated
mirror. Never hand-author metadata only in the index — it rots and can lie.

| Kind | Source of truth | Also in |
|---|---|---|
| Capability (`httpFeatures`, `requires_*`, components, ports) | module **code** (SDK `GetInfo`/requires) → emitted as **OCI image annotations** at build | index entry |
| Install (values overlay, cluster holes) | **`values.yaml`** in the repo | index entry (or referenced) |
| Bundles | module self-describes (`requirements.go`) | index entry |
| Presentation (description, category, docs) | **`module.yaml`** in the repo | index entry |

```
module code ──build──▶ OCI image annotations  ┐
module.yaml (repo) ──────────────────────────  ├─▶ index.yaml (generated mirror)
values.yaml (repo) ──────────────────────────  ┘
```

`tiny` installs off the index for speed but **verifies against the signed image
annotations**, so the index can never quietly lie.

### 3.7 Build & publish with no platform

A module must be buildable and publishable with **zero platform involvement** —
otherwise the platform is still a gate. Today's build uses
`tools build --devkey <TINY_DEV_SECRET>`; that devkey is the last platform
dependency in the build path and **must go**. Decentralized publish is:

```
container build → push to GHCR → emit self-description as OCI annotations
→ cosign sign → `tiny repo index` → host index.yaml (static)
```

No devkey, no platform call. The author owns their image (GHCR) and their index
(a static file they host anywhere).

**The platform discovers passively — pull, never push.** It crawls registered
repo indexes (plus a `tinysystems-module` GitHub-topic convention and an "add
your repo URL" submission, à la Artifact Hub / Homebrew taps) and merges them
into its search index. The module author does nothing platform-specific; the
platform just notices. "Passive" means pull-from-a-registered-set + a discovery
convention — not magic whole-internet crawling — but registration is open to
anyone and gates nothing.

## 4. Schemas

### 4.1 `index.yaml` (repo)
```yaml
apiVersion: tiny/v2
generated: 2026-07-21T00:00:00Z
modules:
  http-module:
    source: github.com/tiny-systems/http-module
    description: HTTP client + server components
    category: net
    versions:
      - version: 1.4.2
        image: ghcr.io/tiny-systems/http-module:1.4.2
        digest: sha256:…                 # verifiable
        chart: tinysystems-operator
        chartVersion: ">=0.2.0 <0.3.0"
        valuesRef: values.yaml           # in-repo path or inline
        clusterFills: [ingressClass]      # holes this module needs filled
        bundles:
          - name: pgvector
            chart: oci://ghcr.io/tiny-systems/charts/pgvector
            chartVersion: 0.3.1
            defaultOn: false
        cosign: true                      # image + entry are signed
```

### 4.2 `module.yaml` (in the module repo)
Human-authored presentation + bundle declarations that aren't derivable from
code. Everything else (`requires_*`, components) is derived at build and need
not be duplicated here.

### 4.3 OCI image annotations
At build, the module emits its self-described capability metadata as image
annotations (`io.tinysystems.module.requires.ingress=true`, component list,
etc.). Travels with the artifact; verified on install.

## 5. `tiny` CLI

```
tiny repo add <name> <url>       # add a repo (index URL)
tiny repo list                   # configured repos + last update
tiny repo remove <name>
tiny repo update                 # fetch each index → merge → cache

tiny search <query>              # over the local merged index
tiny install <module>[@version] [--bundle a,b | --no-bundles]
tiny install <repo>/<module>     # disambiguate across repos
tiny up                          # runtime + default module set (from default repo)
```

- Bare `<module>` resolves across configured repos (error on ambiguity, name the
  repos). Legacy `-v0` names alias transparently during migration (§8).
- **Offline:** resolve + install run from the cached index; only `repo update`
  needs the network. `--offline` forbids any fetch.
- Cache: `${XDG_CACHE_HOME:-~/.cache}/tiny/repos/<name>/index.yaml`.

## 6. Install flow

1. Resolve name → entry in the **local merged index** (§5).
2. **Verify**: cosign-verify the image digest (and index signature); fail closed
   on a repo that claims `cosign: true`.
3. **Build values**: module `values.yaml` + cluster fills (from `tiny up`
   settings) → merged values.
4. `helm upgrade --install <harnessChart@chartVersion> -f values` (release name =
   module id).
5. For each **selected bundle**: `helm upgrade --install <bundleChart> -f
   bundleValues` (own release). Respect `--no-bundles` / `["none"]`.
6. Record installed version + digest for `tiny status`.

## 7. Versioning

**Decided:** two majors of a module must coexist **in the same cluster and even
the same namespace** (a flow can bind nodes to both). So the major is part of
**runtime identity** — and unlike Go modules (whose majors only coexist at build
time), you cannot omit the suffix for the first major or retro-add it later:
every version's K8s resources need the major in their names from day one.

The clean split keeps that property without polluting the human-facing name:

- **Repo / module name** stays plain — `github.com/tiny-systems/http-module`, no
  suffix. (Transparency.)
- **Image** is one repo, SemVer-tagged — `ghcr.io/tiny-systems/http-module:2.3.1`.
  The major *is* the SemVer major.
- **Coexistence coordinate = `<module>-v<major>`, DERIVED from the SemVer major**
  at install time — used for the helm release name, every K8s resource
  name/label (`app.kubernetes.io/instance=http-module-v2`), and node refs
  (`…http-module-v2.http-server-…`). It is **not** stored in the repo name or the
  image repo.

So `tiny install http-module@2.3.1` → release `http-module-v2`, image
`…/http-module:2.3.1`, coexisting with `http-module-v1` in the same namespace.
Within a major, upgrades replace in place (minor/patch); across majors, separate
releases.

This is exactly what `-v0` was reaching for — the mistake was baking it into the
name/image repo instead of deriving it. It becomes a runtime detail that equals
the SemVer major (so today's `-v0` just means "still 0.x").

### 7.1 Publisher is the second coordinate

**Decided (2026-07):** the major is not the only thing that has to coexist. A
cluster will hold modules from **many publishers**, and nothing stops two of them
shipping a module called `http-module`. With identity as `<module>-v<major>`
alone, both install as release `http-module-v0` with the same `TinyModule` name:
the second install silently upgrades the first. Third-party repos are already
assumed elsewhere in this document (§9.4, signing), so this is an omission, not a
decision to keep one publisher per cluster.

Publisher is therefore a coordinate of exactly the same kind as the major —
**derived at install time, never stored in the repo name or the image repo**:

    releaseName = "<repo>-<module>-v" + semverMajor(version)

So `tiny install acme/http-module@2.3.1` → release `acme-http-module-v2`,
image `ghcr.io/acme/http-module:2.3.1`, coexisting with
`tinysystems-http-module-v2` in the same namespace. `<repo>` is the **local repo
name** from `repos.yaml` — the name the operator of *this* cluster chose when
adding the repo — not the GitHub org. That keeps identity under the cluster
operator's control and stable even if an upstream org renames itself.

**Publisher names may contain dashes** (`tiny-systems` does). We do not restrict
them, because we never reverse-parse this string: it is an opaque identity, and
the only parsing that exists strips a trailing `-v<major>`. The residual risk is
that two different pairs can generate one name (`a-b` + `c` and `a` + `b-c`).
That is handled by truth-in-labels, not by a naming rule:

- `tinysystems.io/repo` and `tinysystems.io/module` are set on install and are
  **authoritative**. Anything needing the publisher reads the label.
- Install **fails loudly** when the computed release name already exists with a
  different repo/module label pair, instead of silently replacing another
  publisher's module.

Node refs follow the release name, as with the major
(`…acme-http-module-v2.http-server-…`).

## 8. Migration

Keep everything working at each step; nothing is a flag-day.

- **Phase 0 — publish.** Per module repo CI: build → push image to **GHCR** →
  **cosign sign** → run `tiny repo index` to emit/update the repo's `index.yaml`
  + `module.yaml`. Harness chart published to the default repo.
- **Phase 1 — client.** `tiny` gains `repo` commands + index resolution +
  cosign verify + bundle install. Default repo baked in. `api.tinysystems.io`
  resolution kept as a **fallback** and `-v0` names kept as **aliases**.
- **Phase 2 — aggregator.** The platform rebuilds its catalog as a crawler that
  indexes known repos; `api.tinysystems.io` becomes *just another repo URL* +
  search UI. Truth now flows repos → platform.
- **Phase 3 — retire.** Drop the private registry, the minted `-v0` ids, and the
  fallback once repos cover everything.

## 9. Open decisions

1. **Index format:** flat `index.yaml` (Helm-style, simplest) vs an OCI artifact
   (co-located with images, signable in one place). Lean: start `index.yaml`.
2. **Default-repo hosting:** GitHub Pages vs GHCR OCI. Both durable; GHCR keeps
   it next to images.
3. ~~Coexistence~~ — **DECIDED (2026-07): yes**, two majors coexist in the same
   cluster *and namespace*. The major is a derived runtime coordinate
   (`<module>-v<major>` from the SemVer major); repo name + image repo stay
   plain. See §7.
3b. ~~Publisher collisions~~ — **DECIDED (2026-07): publisher is a second derived
   coordinate**, `<repo>-<module>-v<major>`, so modules from different
   publishers coexist the same way majors do. Dashes in publisher names are
   allowed; ambiguity is handled by authoritative repo/module labels plus a
   loud failure on collision, not by a naming restriction. See §7.1.
4. **Signing:** cosign **keyless** via GitHub OIDC (no key management) — confirm
   the verification story for third-party repos.
5. **Harness chart source:** confirm it ships in the default repo and modules
   pin a range, replacing the global `operatorVersion` pin.

## 10. What this deletes

- `internal/catalog` as an API client → becomes a repo/index reader.
- `provision.moduleValues`'s `requires_* → --set` interpretation → a values
  merge.
- The minted-id + private-registry + platform-as-gate surface.

Net: **fewer moving parts than today** — no registry to run, no ids to mint, no
gate. A module is `{image, values.yaml, module.yaml}` in a repo; a repo is an
`index.yaml`; `tiny` is a Helm-ish client with a default repo.
