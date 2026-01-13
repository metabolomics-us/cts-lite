# CTS-Lite
A lightweight Chemical Translation Service using the PubChemLite dataset, written in Go

### Website
- https://cts-lite.metabolomics.us/

### Documentation
- Please refer to the [documentation page](https://cts-lite.metabolomics.us/pages/documentation.html) for questions regarding use of the API

### Getting Started
- CTS-Lite was written in Go 1.23.1
- The PubChemLite dataset can be downloaded [here](https://zenodo.org/records/17076905)
- To use the dataset trimmer you will need csvkit, install it with `sudo apt install csvkit`
- Load tests are performed using the [Locust](https://locust.io/) framework 

### Docker
- CTS-Lite consists of two docker images
    - A base image containing the PubChemLite dataset only
    - And the application image built on top of the base image
    - This is done to allow for automatic deployment with GitHub Actions, since the dataset is not on the remote repository

### Attributions
Credit for the PubChemLite dataset goes to PubChemLite, CCSbase, and Zenodo.
