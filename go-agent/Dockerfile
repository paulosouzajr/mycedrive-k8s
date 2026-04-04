FROM golang:1.18 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a fully static binary – no libc dependency so it runs on scratch/alpine.
# Exclude the overlay/lib package (Docker internals, CGo) from the build.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -a -installsuffix cgo \
    -ldflags '-extldflags "-static"' \
    -o go-agent .

# ---- runtime image ----
FROM alpine:3.19

# ca-certificates needed for HTTPS calls to the Migration Coordinator.
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /

COPY --from=builder /app/go-agent /go-agent

# end_container is the preStop hook binary – it is the same binary called
# with the "end_container" sub-command.
RUN ln -s /go-agent /end_container

ENTRYPOINT ["/go-agent"]