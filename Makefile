.PHONY: dev build test sqlc docker-up docker-down fmt tidy

# Local docker Postgres only. The integration test creates and deletes its own user.
TEST_DATABASE_URL ?= postgres://stickynotes:stickynotes@localhost:5433/stickynotes?sslmode=disable

dev:
	go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test ./...

sqlc:
	sqlc generate

docker-up:
	docker compose up -d --wait

docker-down:
	docker compose down

fmt:
	go fmt ./...

tidy:
	go mod tidy
