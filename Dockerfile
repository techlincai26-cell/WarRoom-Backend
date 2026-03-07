FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the Go app
RUN CGO_ENABLED=0 GOOS=linux go build -o main ./cmd/server/main.go

# Final stage
FROM alpine:latest

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/main .

# Copy environment file if it exists (optional for Cloud Run, but good for local dev)
COPY --from=builder /app/.env* ./

# Expose the standard port
EXPOSE 8080

# Command to run the application
CMD ["./main"]
