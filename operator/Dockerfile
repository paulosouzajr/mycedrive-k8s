# Build the MyceDrive operator binary.
# Build context must be the operator/ directory:
#   docker build -t mycedrive/operator:dev operator/
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
RUN go build -o /out/operator .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/operator /operator
USER 65532:65532
ENTRYPOINT ["/operator"]
