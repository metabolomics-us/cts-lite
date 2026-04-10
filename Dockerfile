FROM golang:1.25-alpine

WORKDIR /app
COPY . .

# Build the server
RUN go mod download
RUN go build -o ctslite ./server

EXPOSE 8080
CMD ["./ctslite"]

