# CTS-Lite
A lightweight Chemical Translation Service using a curated subset of <strong>10.6 million compounds</strong> from the PubChem database.

### Project Health
[![CI/CD](https://github.com/metabolomics-us/cts-lite/actions/workflows/cicd.yml/badge.svg)](https://github.com/metabolomics-us/cts-lite/actions/workflows/cicd.yml)
[![Website](https://img.shields.io/website?url=https%3A%2F%2Fcts-lite.metabolomics.us&label=Website)](https://cts-lite.metabolomics.us)
[![Last Commit](https://img.shields.io/github/last-commit/metabolomics-us/cts-lite?label=Last%20Commit)](https://github.com/metabolomics-us/cts-lite/commits/main)

### Website
- https://cts-lite.metabolomics.us/

### API Usage
- Please refer to the [documentation page](https://cts-lite.metabolomics.us/docs) for information regarding the API

### Attributions
- Credit to PubChem (NIH), for the data.

<br>

## Development

### Stack
- Go 1.25
- SQLite
- JavaScript/HTML/CSS
- Docker
- Playwright
- Locust (load testing)

### Docker
- CTS-Lite is containerized with Docker
- The GitHub Actions workflow will automatically build and deploy the complete application image upon any push or merge to the main branch
- Note: to build the docker image locally, you must have the database built and stored as `dataset/compounds.db`
    - `cd dataset && go run cmd/build-db/build-db.go cts-lite.csv compounds.db`

### Dataset Creation
- Simply run the `create_csv_dataset.sh` script found under `cmd/`
- To update the dataset used by production, make sure you elect to push to S3 at the end of the script
    - Then, the next time the app is deployed via GitHub Actions (push/merge to main), the latest dataset will be downloaded from S3 and the database will be rebuilt

