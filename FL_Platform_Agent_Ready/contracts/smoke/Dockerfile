FROM golang@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437
RUN apt-get update && apt-get install -y --no-install-recommends python3-venv \
    && rm -rf /var/lib/apt/lists/*
RUN python3 -m venv /opt/proto-venv
ENV PATH="/opt/proto-venv/bin:/go/bin:${PATH}" GOTOOLCHAIN=local GOMAXPROCS=2
RUN pip install --no-cache-dir grpcio-tools==1.75.1 grpcio==1.75.1 \
    protobuf==6.32.1 setuptools==80.9.0 typing-extensions==4.15.0
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.9 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
WORKDIR /work
CMD ["bash", "/src/contracts/smoke/run.sh"]
