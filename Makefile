.PHONY: run test lint build up down migrate migrate-down

DATABASE_URL ?= postgres://featuresteward:featuresteward@localhost:5432/featuresteward?sslmode=disable

run:
	go run ./cmd/featuresteward

test:
	go test ./...

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

migrate-down:
	go tool goose -dir migrations postgres "$(DATABASE_URL)" down
