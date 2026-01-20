# This is the base image that contains only the CTS-Lite dataset 
# - it is used for the GHA workflow to be able to auto-deploy the app to AWS without storing the dataset on GitHub
FROM golang:1.23-alpine

RUN mkdir -p /base/data

COPY data/ /base/data

ENV CTS_DATA_DIR=/base/data
