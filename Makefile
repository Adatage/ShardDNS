.PHONY: proto build build-all run docker-up docker-down lint clean

VERSION ?= dev
LDFLAGS  = -s -w -X main.version=$(VERSION)

proto:
	buf generate

build:
	@mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/sharddnsd ./cmd/sharddnsd
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/sharddns ./cmd/sharddns

build-all: proto build

run: build
	./bin/sharddnsd

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

lint:
	go vet ./...

clean:
	rm -rf bin
