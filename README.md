# CTS-Lite
A lightweight Chemical Translation Service using an augmented version the PubChemLite dataset, written in Go

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
- The `README.md` inside `./csv_magic` has steps to recreating the augmented dataset

### Docker
- CTS-Lite consists of two docker images
    - A base image containing the dataset only
    - And the application image built on top of the base image
    - This is done to allow for automatic deployment with GitHub Actions, since the dataset is not on the remote repository due to size

### Attributions
Credit for the PubChemLite dataset goes to PubChemLite, CCSbase, and Zenodo.
Credit for the data from PubChem goes to PubChem, NIH.
