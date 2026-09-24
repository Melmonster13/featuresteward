.PHONY: run test test-int lint build up down migrate migrate-down admin generate

DATABASE_URL ?= postgres://featuresteward:featuresteward@localhost:5432/featuresteward?sslmode=disable
SQLC_IMAGE := sqlc/sqlc:1.31.1@sha256:70f53171d27b2424e9358869975455a6e955a5aa8e58a998a270a6e34e525537

run:
	go run ./cmd/featuresteward

test:
	go test ./...

# Needs Postgres running (make up, or docker compose up -d postgres).
test-int:
	TEST_DATABASE_URL="$(DATABASE_URL)" go test -tags integration ./...

generate:
	docker run --rm -v "$(CURDIR)":/src -w /src $(SQLC_IMAGE) generate

lint:
	go vet ./...

build:
	docker build -t featuresteward .

up:
	docker compose up --build

down:
	docker compose down

migrate:
	go tool goose -dir migrations postgres "$(DATABASE_URL)" up

# Usage: make admin HANDLE=mel
admin:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/featuresteward create-admin --handle "$(HANDLE)"

migrate-down:
	go tool goose -dir migrations postgres "$(DATABASE_URL)" down
