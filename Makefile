BINARY := wavonyx
PKG    := ./cmd/wavonyx
DIST   := bin

.PHONY: all build run test lint tidy clean docker compose-up compose-down

all: build

build:
	@mkdir -p $(DIST)
	go build -trimpath -ldflags="-s -w" -o $(DIST)/$(BINARY) $(PKG)

run: build
	./$(DIST)/$(BINARY) serve

test:
	go test -v -race ./...

tidy:
	go mod tidy

lint:
	go vet ./...

clean:
	rm -rf $(DIST)

docker:
	docker build -t wavonyx:local .

compose-up: build
	docker compose up -d

compose-down:
	docker compose down
