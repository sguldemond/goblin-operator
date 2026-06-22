FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod .
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /goblin-horn .

FROM alpine:3.21
COPY --from=builder /goblin-horn /goblin-horn
ENTRYPOINT ["/goblin-horn"]
