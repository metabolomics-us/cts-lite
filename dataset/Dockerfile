# This is the base image that contains only the CTS-Lite dataset 
# - It is used by the GitHub Actions workflow, allowing auto-deployment to AWS without storing the dataset on GitHub
FROM golang:1.23-alpine

ENV DATASET=cts-lite_20260410.csv
ENV CTS_DATA_DIR=/base/data

RUN mkdir -p ${CTS_DATA_DIR}

# Copy the dataset
COPY ${DATASET} ${CTS_DATA_DIR}
COPY cmd /cmd

# Build the database
RUN go run ./cmd/build-db/build-db.go "${CTS_DATA_DIR}/${DATASET}" "${CTS_DATA_DIR}/compounds.db"

