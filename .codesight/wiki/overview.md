# cts-lite — Overview

> **Navigation aid.** This article shows WHERE things live (routes, models, files). Read actual source files before implementing new features or making changes.

**cts-lite** is a go project built with go-net-http.

## Scale

8 API routes · 8 library files · 4 environment variables

## Subsystems

- **[Docs](./docs.md)** — 1 routes — touches: db
- **[Documentation](./documentation.md)** — 1 routes — touches: db
- **[Match](./match.md)** — 1 routes — touches: db
- **[Pages](./pages.md)** — 2 routes — touches: db
- **[Infra](./infra.md)** — 3 routes — touches: db

## High-Impact Files

Changes to these files have the widest blast radius across the codebase:

- `ctslite/model` — imported by **10** files
- `net/http` — imported by **9** files
- `encoding/csv` — imported by **7** files
- `database/sql` — imported by **5** files
- `encoding/json` — imported by **4** files
- `net/http/httptest` — imported by **3** files

## Required Environment Variables

- `CI` — `playwright/playwright.config.js`
- `DB_PATH` — `server/main.go`
- `GITHUB_ACTIONS` — `playwright/playwright.config.js`
- `PORT` — `server/main.go`

---
_Back to [index.md](./index.md) · Generated 2026-06-18_