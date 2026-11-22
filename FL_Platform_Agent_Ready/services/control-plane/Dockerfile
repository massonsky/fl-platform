FROM fl-platform/proto-tools:arch03 AS build
WORKDIR /work
COPY services/control-plane/go.mod services/control-plane/go.sum ./
RUN go mod download
COPY services/control-plane/ ./
RUN go mod tidy && go test ./... && go vet ./...
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api && CGO_ENABLED=0 go build -o /out/orchestrator ./cmd/orchestrator && CGO_ENABLED=0 go build -o /out/bootstrap ./cmd/bootstrap
FROM debian@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/ /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/api"]
