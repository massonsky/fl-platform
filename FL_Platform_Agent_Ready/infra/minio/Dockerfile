FROM golang@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437 AS build
ARG MINIO_RELEASE=RELEASE.2025-10-15T17-29-55Z
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOMAXPROCS=2
WORKDIR /src
RUN git clone --depth 1 --branch "$MINIO_RELEASE" https://github.com/minio/minio.git .
RUN test "$(git rev-parse HEAD)" = "9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a"
RUN go build -p 2 -trimpath -o /out/minio .

FROM debian@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/minio /usr/local/bin/minio
COPY --from=build /src/LICENSE /usr/share/doc/minio/LICENSE
COPY entrypoint.sh /opt/fl-entrypoint.sh
EXPOSE 9000 9001
VOLUME /data
