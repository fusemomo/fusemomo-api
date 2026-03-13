## ⚡ What is Fusemomo?

AI agents are stateless by default. Every time an agent runs, it usually starts blind, making the same wrong decisions repeatedly. 

Fusemomo is domain-agnostic and solves this by acting as an intelligent prediction layer built explicitly for action-taking agents. It provides three core layers:

1. **L1 — Identity Resolution:** Resolves the same entity across every connected API into one canonical record, using both deterministic and probabilistic (trigram similarity) fuzzy matching.
2. **L2 — Behavioral Graph:** An immutable, append-only log of every action an agent has taken on any entity through any API.
3. **L3 — Behavioral Intelligence (Next Best Action):** Before an agent acts, it queries Fusemomo for the highest-success action type relative to its intent based on historical interactions.

---

## 🛠 Technical Stack

The backend API is designed using a clean three-layer architecture (Handler → Service → Repository). 

- **Language:** Go `1.25.1`
- **Framework:** Fiber `v3`
- **Primary Database:** PostgreSQL 17 via Supabase 
- **Driver:** `pgx/v5`
- **Validations:** `validator/v10`
- **Auth:** SHA-256 hashed API Keys & Supabase JWT via `golang-jwt/v5`
- **OpenAPI:** `swaggo/swag` auto-generation

---

## 💾 Database Schema 

FuseMomo's backend runs heavily strictly typed tables with full integration for Row Level Security (RLS) and Role-Based Access Control (RBAC).

| Table | Purpose |
|---|---|
| `tenants` | One row per FuseMomo customer. Linked via Supabase Auth. Root of data isolation. |
| `api_keys` | Programmatic credentials for agents with stored SHA-256 hashes. Bypasses RLS. |
| `entities` | The canonical domain-agnostic record for any observed entity (person, repo, ticket). |
| `entity_identifiers` | L1 Graph: Stores deterministic and probabilistic mapping of identifiers across APIs. |
| `entity_links` | The audit trail mapping of merged entity events. |
| `interactions` | L2 Graph: Highly-indexed, append-only immutable event log of all API attempts and outcomes. |
| `recommendations` | L3 Audit Log: Tracks the Next Best Action quality over time to close the feedback loop. |
| `webhook_endpoints` | Tenant-registered URLs for real-time event delivery. |
| `webhook_events` | Per-attempt delivery log featuring automatic retry tracking. |
| `usage_logs` | Monthly usage counters for usage-based billing limitations. |

---

## 🌐 API Routes Reference

FuseMomo serves explicit operations via pragmatically versioned endpoints (`/v1`).

### Core Interoperability (Agent Tools)
*Requires standard API Key authentication (`Bearer <key>`)*
- `GET    /v1/core/entities` - List entities globally
- `GET    /v1/core/entities/:id` - Fetch singular entity summary
- `DELETE /v1/core/entities/:id` - GDP erasure/redact anonymity 
- `POST   /v1/core/entities/resolve` - Search or dynamically create an entity profile via identifier
- `POST   /v1/core/entities/:id/link` - Forcefully merge identities
- `POST   /v1/core/interactions/log` - Log a single behavioral interaction
- `POST   /v1/core/interactions/batch` - Batch submit multiple interaction events
- `POST   /v1/core/recommends` - Get "Next Best Action" recommendation given an intent
- `PATCH  /v1/core/recommends/:id/outcomes` - Push final execution outcome for scoring feedback

### Dashboard (Tenant Administration)
*Requires Supabase JWT Auth & active `user` / `admin` roles.*
- `GET    /v1/dashboard/profile` - Fetch tenant subscription and constraints
- `PATCH  /v1/dashboard/profile` - Update profile configurations
- `DELETE /v1/dashboard/` - Tenant account deletion
- `GET    /v1/dashboard/usage` - Current metrics (resolution count, limits)
- `GET    /v1/dashboard/usage/history` - Monthly billing retention metrics
- `GET    /v1/dashboard/recommendations` - Paginated insights of intelligence success queries
- `GET    /v1/dashboard/recommendations/stats` - Recommendation success and improvement metrics

### API Key Management
*Requires Supabase JWT Auth.*
- `GET    /v1/app/key` - Fetch individual key usage
- `GET    /v1/app/key/all` - List all generated credentials
- `POST   /v1/app/key/create` - Provision new key for agent systems
- `DELETE /v1/app/key/:id` - Revoke and explicitly delete API key
- `POST   /v1/app/key/:id/revoke` - Revoke activity but preserve audit history
- `POST   /v1/app/key/sync-expired` - Cron manual job for expiration state maintenance

### Auth & Global Operations
- `GET    /v1/auth/login/:provider` - OAuth Provider Handshake (Google, GitHub)
- `GET    /ping` - Health Check (`pong`)
- `GET    /health` - Deeper structural DB ping 

### Admin Operations
*Requires Supabase JWT Auth with strict `admin` role.*
- `GET    /v1/admin/tenants` - Display all registered users
- `PATCH  /v1/admin/tenants/:id/plan` - Direct plan overrides
- `DELETE /v1/admin/tenants/:id` - Prune tenant profile
- `GET    /v1/admin/usage/global` - Full macro platform usage metrics

---

## 🚀 Getting Started

These instructions will get you a copy of the project up and running on your local machine for development and testing purposes. 

Ensure a `.env` file is placed in your root directory containing your Supabase instances (`DATABASE_URL`, `SUPABASE_JWT_JWK`, etc)

### Makefile Commands

Run build make command with tests
```bash
make all
```

Build the Go application via `cmd/api`
```bash
make build
```

Run the application locally
```bash
make run
```

Live reload the application (requires package installer like Air):
```bash
make watch
```

Run the `testify` test suite:
```bash
make test
```

Clean up binary from the last build:
```bash
make clean
```
