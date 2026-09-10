FROM golang:1.22-alpine AS builder

RUN apk add --no-libc-dev --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -a -ldflags '-linkmode external -extldflags "-static"' -o portal main.go

FROM alpine:latest
RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/portal /app/portal
COPY --from=builder /app/public /app/public

EXPOSE 8080
ENTRYPOINT ["/app/portal"]
