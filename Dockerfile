# Build Stage
FROM golang:1.23-alpine AS builder

LABEL authors="the-eduardo"

WORKDIR /app

COPY . .

RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /go/bin/app ./...

# Final stage
FROM alpine:latest

WORKDIR /app

# Copy only the built binary from the builder stage
COPY --from=builder /go/bin/app /app/

# Command to run the executable
CMD ["./app"]