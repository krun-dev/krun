.PHONY: build test check-secrets check-static check-static-build dev dev-down runtime-image runtime-config clean
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

test:
	./scripts/check-secrets.sh
	python3 scripts/check-static-console.py
	go test -race ./...
	go vet ./...
	npm run check
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
