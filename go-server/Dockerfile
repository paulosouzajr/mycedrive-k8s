FROM golang:1.18 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build the Go application statically to ensure compatibility with Alpine
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

FROM alpine:latest

WORKDIR /root/

# Install necessary certificates
RUN apk --no-cache add ca-certificates

COPY --from=builder /app/main .

CMD ["./main"]