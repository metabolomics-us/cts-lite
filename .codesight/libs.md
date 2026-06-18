# Libraries

- `api/handler.go` — function Match: (index *model.PubChemIndex, w http.ResponseWriter, r *http.Request), function Status: (w http.ResponseWriter, _ *http.Request)
- `api/load_tests/locustfile.py` — function on_locust_init: (environment, **kwargs), class CTSLiteUser
- `dataset/cmd/pubchem-fetcher/fetcher.go` — class Property, class PugResponse
- `model/model.go`
  - function OpenSQLiteIndex: (dbPath string) (*PubChemIndex, error)
  - class ClassyFireInfo
  - class Compound
  - class SingleResult
  - class PubChemIndex
- `model/testing.go` — function LoadCSVToMemory: (csvPath string) (*PubChemIndex, error), function LoadCSVToPrivateMemory: (csvPath string) (*PubChemIndex, error)
- `rdkit/rdkit.go` — function SmilesToInChIKey: (smiles string) (string, error)
- `telemetry/instrumentation.go`
  - function RecordMatch: (r *http.Request, results []*model.SingleResult, matchCount int, duration time.Duration, opts MatchOptions)
  - function RecordClassyFireOutcomes: (ctx context.Context, classified, notFound, failed int)
  - class MatchOptions
- `telemetry/telemetry.go` — function Setup: (ctx context.Context) (shutdown func(context.Context) error, err error)
