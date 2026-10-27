.PHONY: api ui test build

api:
	go run ./cmd/cctv-sub

ui:
	npm run dev

test:
	go test ./cmd/... ./internal/...
	npm test

build:
	go build -o bin/cctv-sub ./cmd/cctv-sub
	npm run build
