FROM golang:1.22-alpine AS builder

RUN apk add --no-libc-dev --no-cache gcc musl-dev git nodejs npm

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -a -ldflags '-linkmode external -extldflags "-static"' -o orchestrator main.go

FROM alpine:latest
RUN apk add --no-cache ca-certificates git nodejs npm bash

WORKDIR /app
COPY --from=builder /app/orchestrator /app/orchestrator
COPY --from=builder /app/assets /app/assets
COPY --from=builder /app/scripts /app/scripts

EXPOSE 3000
ENTRYPOINT ["/app/orchestrator"]
