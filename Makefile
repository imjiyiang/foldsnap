.PHONY: build web run dev test check package clean
web:
	cd apps/web && npm ci && npm run build
build: web
	rm -rf internal/webui/assets
	mkdir -p internal/webui/assets bin
	touch internal/webui/assets/.keep
	cp -R apps/web/dist/. internal/webui/assets/
	go build -trimpath -o bin/localbackup ./cmd/localbackup
run: build
	./bin/localbackup -data "$(CURDIR)/.data" -addr 127.0.0.1:18787 -open
dev:
	./scripts/dev.sh
test:
	go test -race ./...
check:
	go vet ./...
	cd apps/web && npm run build
package: build
	./packaging/macos/package.sh
clean:
	rm -rf bin apps/web/dist release
