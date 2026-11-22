ROOT := $(CURDIR)
COMPOSE := docker compose -f compose.yaml -f compose.runtime.yaml

.PHONY: tools build test lint fmt proto up logs
tools:
	docker build -t fl-platform/proto-tools:arch03 contracts/smoke
	docker build -t fl-platform/http-checks:arch04 contracts/http/checks
	docker build -t fl-platform/ml-base:mvp -f services/ml-client/Dockerfile.base .
build:
	docker build -t fl-platform/control-plane:mvp -f services/control-plane/Dockerfile .
	docker build -t fl-platform/ml-client:mvp -f services/ml-client/Dockerfile .
	docker build -t fl-platform/web:mvp -f web/Dockerfile .
test:
	docker build --target build -t fl-platform/go-test:mvp -f services/control-plane/Dockerfile .
	docker run --rm -v "$(ROOT):/src:ro" -e PYTHONPATH=/src/services/ml-client/src fl-platform/ml-base:mvp python -m unittest discover -s /src/services/ml-client/tests -v
	docker run --rm -v "$(ROOT):/src:ro" fl-platform/http-checks:arch04
	docker run --rm -v "$(ROOT):/src:ro" fl-platform/proto-tools:arch03
	python3 -m unittest discover -s tests/integration -p test_credential_retries.py -v
	docker run --rm --network none -v "$(ROOT):/src:ro" -w /src node@sha256:e9f565d32859f0cbfb52125eafe8be39390f24dac302dc3cdf566e8ba3ab21ee node --test tests/e2e/credential-retry.test.cjs
	python3 scripts/check_code_size.py .
lint:
	docker build --target build -t fl-platform/go-test:mvp -f services/control-plane/Dockerfile .
	docker build -t fl-platform/web:mvp -f web/Dockerfile .
fmt:
	cd services/control-plane && gofmt -w cmd internal
proto:
	docker run --rm -v "$(ROOT):/src" --entrypoint bash fl-platform/proto-tools:arch03 /src/scripts/generate_proto.sh
up:
	$(COMPOSE) up -d
logs:
	$(COMPOSE) logs --tail 50 api orchestrator worker0
