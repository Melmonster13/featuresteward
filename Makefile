.PHONY: run test lint build up down

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
