# go-mathsearch

A search engine for mathematical formulas, built on
[go-canonical](../go-canonical.git). It indexes a corpus of Mathematica-syntax
formulas and answers three kinds of query:

- **Exact** — by canonical **SHA-256 signature**, so a match is returned for any
  formula equal modulo associative/commutative reordering, bound-variable
  (α) renaming, and free-variable renaming.
- **Fuzzy** — by structural similarity (shared subexpressions, operator paths),
  retrieved with SQLite FTS5/BM25 and re-ranked by set overlap.
- **Progressive faceted filter** — narrow by structure ("is an integral", "has a
  square root", "has a Bessel function", …) until the result set is small enough
  to list. This is the primary discovery UX.

Results are **de-duplicated by signature**: the same formula appearing in DLMF
and G&R collapses to one hit that lists every occurrence.

Each identity can also carry a **formal proof** blob (Lean 4 / Coq / Rocq).

## Architecture

```
Mathematica text ──canonical.Parse──▶ canonical form ──▶ SHA-256 signature (exact key)
                                             │
                                     feature tokens ──▶ FTS5 (fuzzy) + facets (filter)
                                             │
        SQLite  ◀────────────────────────────┘   HTTP API + KaTeX web UI + Swagger
```

- **go-canonical** parses Mathematica syntax, normalizes, and produces the
  canonical form and signature (the equality oracle).
- **SQLite** (pure-Go `modernc.org/sqlite`, FTS5) stores formulas, feature
  tokens, facets, and proofs. Ingest is incremental (upsert by entry id) and
  batched; a stored engine fingerprint triggers a rebuild when go-canonical
  changes.
- Package layout: `feature/` (canonical-string reader → tokens & facets),
  `store/` (schema, ingest, exact/fuzzy/facet/dedup queries), `auth/` (JWT,
  OAuth, rate limiting), `config/` (JSON config), `cmd/mathsearch/` (CLI + HTTP).

## Quick start

```
make build
./bin/mathsearch ingest -db mathsearch.db  <corpus.json | dir> ...
./bin/mathsearch serve  -db mathsearch.db  -addr :8080
```

Then open <http://localhost:8080/> for the UI (progressive filter + formula
search) and <http://localhost:8080/docs> for the Swagger API docs.

CLI search and the duplicate report:

```
mathsearch search -db mathsearch.db "Integrate[E^(-a x^2), {x, 0, Infinity}]"
mathsearch dedup  -db mathsearch.db
```

## Configuration

Everything is driven by a JSON config (see `config.example.json`), loaded with
`-config`. Flags (`-db`, `-addr`) override it; `MATHSEARCH_JWT_SECRET` overrides
the JWT secret from the environment.

```
mathsearch serve -config config.json
```

## HTTP API

Public: `GET /api/search?q=`, `GET /api/facets?f=…&f=…`, `GET /api/stats`,
`GET /api/proofs/{id}`. Authenticated (write): `POST /api/formulas`,
`PUT /api/proofs/{id}`. The full OpenAPI 3 spec is served at
`/api/openapi.json` and rendered at `/docs`.

## Authentication and rate limiting

Two classes of user, both carrying the same signed JWT (distinguished by a role
claim):

- **Privileged** — username/password (`POST /api/login`) against config `users`
  (bcrypt hashes; generate with `mathsearch hashpw`). Full access, exempt from
  rate limiting.
- **Social** — OAuth via Google / Facebook / Apple
  (`GET /auth/{provider}` → provider → `GET /auth/{provider}/callback`), which
  issues a **rate-limited** token. Provider credentials go in the config; Apple
  additionally needs its ES256-signed client secret. (Social login needs real
  credentials to exercise end to end.)

A token-bucket **rate limiter** guards `/api/` per identity (per-user when
authenticated, per-IP otherwise); privileged tokens are exempt.

## Formal proofs

Each entry may carry a proof blob:

```
mathsearch proof -db mathsearch.db dlmf-1.11.12 -set proof.lean -system lean4 -status verified
mathsearch proof -db mathsearch.db dlmf-1.11.12          # print it
```

or via `PUT /api/proofs/{id}` (authenticated).

## Development

```
make            # fmt + vet + test
make test-race
make ingest CORPUS=<dir> DB=mathsearch.db
make serve  DB=mathsearch.db ADDR=:8080
```

The module uses a local `replace sorins/canonical => ../go-canonical.git`.
