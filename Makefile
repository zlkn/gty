.PHONY: test build deb

test:
	go test ./...

build:
	CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -o bin/gty .

deb: build
	./packaging/deb/build.sh
