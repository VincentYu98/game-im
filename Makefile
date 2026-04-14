PROTO_DIR   := api/proto
GEN_DIR     := api/pb
PROTO_FILES := $(wildcard $(PROTO_DIR)/*.proto)
BINARY      := bin/im-server

.PHONY: proto build run test bench clean

proto:
	@mkdir -p $(GEN_DIR)
	protoc \
	  --proto_path=$(PROTO_DIR) \
	  --go_out=$(GEN_DIR) \
	  --go_opt=paths=source_relative \
	  $(PROTO_FILES)

build:
	go build -o $(BINARY) ./cmd/server
	go build -o bin/im-bench ./cmd/bench

run:
	go run ./cmd/server --config configs/config.yaml

test:
	go test ./... -race -count=1

bench:
	go build -o bin/im-bench ./cmd/bench
	@echo "Run: ./bin/im-bench --conns 50 --msgs 10 --addr localhost:9000"

clean:
	rm -rf bin/

dev-deps:
	docker-compose up -d

dev-deps-down:
	docker-compose down
