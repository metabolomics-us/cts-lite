# CTS-Lite
A lightweight Chemical Translation Service matching against a curated subset of the PubChem database.

### Website
- https://cts-lite.metabolomics.us/

### API Documentation
- Please refer to the [documentation page](https://cts-lite.metabolomics.us/pages/documentation.html) for questions regarding use of the API

### Attributions
- Credit to PubChem (NIH), for the data.

---

## Development

### Stack
- Go 1.25
- SQLite
- Docker
- Locust (load testing)

### Docker
- CTS-Lite is containerized with Docker
- The GitHub Actions workflow will automatically build and deploy the complete application image upon any push or merge to the main branch
- Note: to build the docker image locally, you must have the database built and stored as `dataset/compounds.db`
    - `cd dataset && go run cmd/build-db/build-db.go cts-lite.csv compounds.db`

### Dataset Directory
The dataset directory stores the dataset and the tools used to create it

#### Dataset Creation
Simply run the `create_csv_dataset.sh` script found under `cmd/`  
To update the dataset used by production, make sure you elect to push to S3 at the end of the script  
Then, the next time the app is deployed via GitHub Actions (push/merge to main), the latest dataset will be downloaded from S3 and the database will be rebuilt  

#### cmd
The cmd directory holds helper programs and scripts used to construct the dataset and database

