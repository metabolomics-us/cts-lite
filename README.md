# CTS-Lite
A lightweight Chemical Translation Service matching against a curated subset of the PubChem database.

### Website
- https://cts-lite.metabolomics.us/

### API Documentation
- Please refer to the [documentation page](https://cts-lite.metabolomics.us/pages/documentation.html) for questions regarding use of the API

### Attributions
- Credit to PubChem (NIH), for the data.

---

### Development

#### Stack
- Go 1.23
- SQLite
- Docker
- Locust (load testing)

#### Docker
- CTS-Lite is made up of two docker images
    1. A base image containing only the dataset
    2. The complete application image built on top of the base image
        - This is done to allow for automatic deployment with GitHub Actions, since the dataset is not tracked in the repository

- To build the images, first build the base image
    - Ensure the csv dataset is inside `./dataset/` and referenced accordingly by the Dockerfile in `./dataset/`
    - Inside `./dataset/`, run `docker build -t cts-lite:dataset-only .`
    - Push that image to the AWS ECR registry
    - The GitHub Actions workflow will automatically build and deploy the complete application image upon any push to the main branch
    - Note: to build the application image locally, just run `docker build -t cts-lite .` from the root of the project

