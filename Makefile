.PHONY: proto build build-all run docker-up docker-down test test-integration lint bench clean

VERSION ?= dev
LDFLAGS  = -s -w -X main.version=$(VERSION)

proto:
	buf generate

build:
	@mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/sharddnsd ./cmd/sharddnsd
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/sharddns ./cmd/sharddns
	go build -trimpath -o bin/dnsbench ./bench

build-all: proto build

run: build
	./bin/sharddnsd

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

test:
	go test ./...

test-integration:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 -p 1 ./...

lint:
	go vet ./...

bench: build
	./bin/dnsbench -seed -names 10000
	./bin/dnsbench -duration 15s -c 64

clean:
	rm -rf bin
