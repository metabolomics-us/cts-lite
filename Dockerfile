FROM 702514165722.dkr.ecr.us-west-2.amazonaws.com/cts-lite:dataset-only

WORKDIR /app
COPY . .

RUN go mod download
RUN go build -o ctslite ./server

EXPOSE 8080
CMD ["./ctslite"]
