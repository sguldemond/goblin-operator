# Production build: multi-stage from source
FROM golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /goblin-scout .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /goblin-scout /goblin-scout
ENTRYPOINT ["/goblin-scout"]

# Dev build: pre-built binary (faster, avoids pulling golang image)
# Usage: podman build -f Dockerfile.dev -t goblin-scout:dev .
