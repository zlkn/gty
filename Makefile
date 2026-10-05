.PHONY: test build deb clean

test:
	go test -v ./...

build:
	CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -o bin/gty .

deb: build
	./packaging/deb/build.sh

clean:
	rm -rf bin
