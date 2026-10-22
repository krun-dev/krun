.PHONY: build test dev dev-down runtime-image runtime-config clean
build:
	npm ci
	npm run build
	go build -trimpath -o bin/krun ./cmd/krun
	go build -trimpath -o bin/krun-server ./cmd/krun-server

test:
	go test -race ./...
	go vet ./...
	npm run check
	npm run test:permissions
	npm run test:i18n

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
