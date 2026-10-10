.PHONY: build test dev clean
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

dev: build
	./scripts/dev-server.sh

clean:
	rm -rf bin dist
