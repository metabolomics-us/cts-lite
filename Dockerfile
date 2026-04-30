# syntax=docker/dockerfile:1.7
FROM golang:1.25-alpine

WORKDIR /app

# Copy the database in its own layer to improve caching when pushing to ECR
COPY dataset/compounds.db ./dataset/compounds.db
COPY --exclude=dataset/compounds.db . .

# Build the server binary
RUN go mod download
RUN go build -o ctslite ./server

EXPOSE 8080
CMD ["./ctslite"]

