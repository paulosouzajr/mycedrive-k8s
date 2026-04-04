FROM golang:1.18 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binary – no libc required at runtime.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -a -installsuffix cgo \
    -ldflags '-extldflags "-static"' \
    -o go-server .

# ---- runtime image ----
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

# go-server writes logs under /root/tmp
RUN mkdir -p /root/tmp
WORKDIR /root

COPY --from=builder /app/go-server .

EXPOSE 80

ENTRYPOINT ["./go-server"]