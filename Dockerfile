# Build stage
FROM golang:1.24-alpine AS builder

# Install git if required for go modules.
RUN apk add --no-cache git

WORKDIR /app

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code and build.
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o sentry-forwarding .

# Deployment stage
FROM alpine:3

# Install CA certificates for HTTPS requests.
RUN apk --no-cache add ca-certificates

WORKDIR /app/

COPY --from=builder /app/sentry-forwarding .

EXPOSE 8000

CMD ["./sentry-forwarding"]