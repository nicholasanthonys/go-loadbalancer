# deploy/Dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /gobalance ./cmd/gobalance

FROM gcr.io/distroless/static-debian12
COPY --from=build /gobalance /gobalance
COPY configs/docker.yaml /configs/docker.yaml
EXPOSE 9090 9091 9100
ENTRYPOINT ["/gobalance", "-config=/configs/docker.yaml", "-tls=false"]
