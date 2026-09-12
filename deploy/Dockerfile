# deploy/Dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /gobalance ./cmd/gobalance

# scratch needs no image pull at all (unlike gcr.io/distroless, which some
# networks can't reach) and is smaller still; fine here since the demo runs
# with -tls=false and gobalance makes no outbound HTTPS calls that would need
# a CA bundle.
FROM scratch
COPY --from=build /gobalance /gobalance
COPY configs/docker.yaml /configs/docker.yaml
EXPOSE 9090 9091 9100
ENTRYPOINT ["/gobalance", "-config=/configs/docker.yaml", "-tls=false"]
