# This is the base image that contains only the CTS-Lite dataset 
# - It is used by the GitHub Actions workflow, allowing auto-deployment to AWS without storing the dataset on GitHub
FROM golang:1.23-alpine

RUN mkdir -p /base/data
ENV CTS_DATA_DIR=/base/data

# Copy the dataset
COPY cts-lite.csv /base/data
COPY cmd /src/cmd

# Build the database
RUN go run ./src/cmd/build-db/build-db.go "${CTS_DATA_DIR}/cts-lite.csv" "${CTS_DATA_DIR}/compounds.db"

