.PHONY: proto build run docker-up docker-down schema test lint clean install

BINARY_SERVER = bin/sharddns
BINARY_CLI    = bin/sharddns-cli

proto:
	buf generate

build:
	@mkdir -p bin
	go build -ldflags="-s -w" -o $(BINARY_SERVER) ./cmd/server
	go build -ldflags="-s -w" -o $(BINARY_CLI) ./cmd/cli

build-all: proto build

run: build
	./$(BINARY_SERVER)

docker-up:
	docker compose up -d

docker-down:
	docker compose down

schema:
	docker compose exec -T scylla cqlsh -f /cql/database.cql

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -rf bin api
