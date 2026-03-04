# This is the base image that contains only the CTS-Lite dataset 
# - It is used by the GitHub Actions workflow, allowing auto-deployment to AWS without storing the dataset on GitHub
FROM golang:1.23-alpine

RUN mkdir -p /base/data

# Ensure the dataset path and filename are correct
COPY pcl20260102_and_pcms20260116.csv /base/data

ENV CTS_DATA_DIR=/base/data
