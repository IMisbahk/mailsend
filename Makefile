.PHONY: build test package

build:
	mkdir -p build
	CGO_ENABLED=0 go build -trimpath -o build/mailsend ./cmd/mailsend

test:
	go test ./...

package:
	./scripts/build-release.sh
