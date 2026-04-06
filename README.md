# CTS-Lite
A lightweight Chemical Translation Service using an augmented version of the PubChemLite dataset

### Website
- https://cts-lite.metabolomics.us/

### API Documentation
- Please refer to the [documentation page](https://cts-lite.metabolomics.us/pages/documentation.html) for questions regarding use of the API

### Getting Started
- CTS-Lite was written in Go 1.23.1
- The PubChemLite dataset can be downloaded [here](https://zenodo.org/records/18169629)
- To use the dataset trimming script, you will need csvkit. Install it with `sudo apt install csvkit`
- Load tests are performed using the [Locust](https://locust.io/) framework 

### Additional Data
- CTS-Lite uses PubChemLite and an additional subset of PubChem as its dataset
- The `README.md` inside `./data` has steps to recreate the augmented dataset

### Docker
- CTS-Lite is made up of two docker images
    1. A base image containing only the dataset
    2. The complete application image built on top of the base image
        - This is done to allow for automatic deployment with GitHub Actions, since the dataset is not tracked in the repository

- To build the images, first build the base image
    - Ensure the dataset is inside `./data/` and referenced accordingly by the Dockerfile in `./data/`
    - Inside `./data/`, run `docker build -t cts-lite:dataset-only .`
    - Push that image to the AWS ECR registry
    - GitHub actions will automatically build and deploy the complete application image to ECR upon any push to the main branch
    - Note: to build the application image locally, just run `docker build -t cts-lite .` from the root of the project

### Attributions
- Credit for the PubChemLite dataset goes to PubChemLite, CCSbase, and Zenodo.
- Credit for the data from PubChem goes to PubChem, NIH.
