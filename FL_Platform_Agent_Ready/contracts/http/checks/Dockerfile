FROM golang@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437
RUN apt-get update && apt-get install -y --no-install-recommends python3-venv \
    && rm -rf /var/lib/apt/lists/*
RUN python3 -m venv /opt/http-venv
ENV PATH="/opt/http-venv/bin:${PATH}"
RUN pip install --no-cache-dir openapi-spec-validator==0.7.2 \
    openapi-schema-validator==0.6.3 jsonschema==4.23.0 PyYAML==6.0.2
WORKDIR /src/contracts/http/checks
CMD ["python", "-m", "unittest", "-v", "test_contract.py"]
