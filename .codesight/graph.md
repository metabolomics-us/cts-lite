# Dependency Graph

## Most Imported Files (change these carefully)

- `ctslite/model` — imported by **10** files
- `net/http` — imported by **9** files
- `encoding/csv` — imported by **7** files
- `database/sql` — imported by **5** files
- `encoding/json` — imported by **4** files
- `net/http/httptest` — imported by **3** files
- `ctslite/telemetry` — imported by **3** files
- `sync/atomic` — imported by **2** files
- `ctslite/rdkit` — imported by **1** files
- `net/url` — imported by **1** files
- `ctslite/api` — imported by **1** files

## Import Map (who imports what)

- `ctslite/model` ← `api/api_test.go`, `api/classyfire.go`, `api/classyfire_test.go`, `api/handler.go`, `api/match.go` +5 more
- `net/http` ← `api/api_test.go`, `api/classyfire.go`, `api/classyfire_test.go`, `api/handler.go`, `dataset/cmd/pubchem-fetcher/fetcher.go` +4 more
- `encoding/csv` ← `api/api_test.go`, `api/handler.go`, `dataset/cmd/build-db/build-db.go`, `dataset/cmd/build-db/build-db_test.go`, `dataset/cmd/csv-magic/dedupe/dedupe.go` +2 more
- `database/sql` ← `dataset/cmd/build-db/build-db.go`, `dataset/cmd/build-db/build-db_test.go`, `model/model.go`, `model/model_test.go`, `model/testing.go`
- `encoding/json` ← `api/api_test.go`, `api/classyfire.go`, `api/handler.go`, `dataset/cmd/pubchem-fetcher/fetcher.go`
- `net/http/httptest` ← `api/api_test.go`, `telemetry/instrumentation_test.go`, `telemetry/telemetry_test.go`
- `ctslite/telemetry` ← `api/classyfire.go`, `api/handler.go`, `server/main.go`
- `sync/atomic` ← `api/classyfire.go`, `api/classyfire_test.go`
- `ctslite/rdkit` ← `api/match.go`
- `net/url` ← `dataset/cmd/pubchem-fetcher/fetcher.go`
