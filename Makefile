.PHONY: build test test-go check-secrets check-static check-static-build check-storage-models dev dev-down runtime-image runtime-config clean
build:
	npm ci
	npm run build
	go build -trimpath -o bin/krun ./cmd/krun
	go build -trimpath -o bin/krun-server ./cmd/krun-server

check-secrets:
	./scripts/check-secrets.sh

check-static:
	python3 scripts/check-static-console.py

check-static-build:
	python3 scripts/check-static-console.py --dist

check-storage-models:
	python3 scripts/gen-storage-models.py --check

test-go:
	@if [ -z "$${KRUN_TEST_POSTGRES_URL:-}" ]; then \
		echo "Set KRUN_TEST_POSTGRES_URL to a dedicated PostgreSQL test database. See docs/verification.md." >&2; \
		exit 1; \
	fi
	go test -race ./...

test: test-go
	python3 scripts/gen-storage-models.py --check
	./scripts/check-secrets.sh
	python3 scripts/check-static-console.py
	npm run check:web-format
	go vet ./...
	npm run check
	npm run check:workers-format
	npm run test:permissions
	npm run test:i18n
	npm --prefix web test
	npm test --prefix examples/workers-fullstack
	npm run build --prefix examples/workers-fullstack
	npm test --prefix workers/builder
	npm test --prefix workers/runtime

dev:
	./scripts/dev-up.sh

dev-down:
	./scripts/dev-down.sh

runtime-image:
	./scripts/dev-runtime-image.sh

runtime-config:
	./scripts/dev-runtime-config.sh

clean:
	rm -rf bin dist
