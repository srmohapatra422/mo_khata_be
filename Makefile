include .env
export

run:
	go run ./cmd/api

migrate-up:
	goose -dir migrations postgres "$(MIGRATE_DATABASE_URL)" up

migrate-new:
	goose -dir migrations create $(name) sql

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -o bin/api ./cmd/api
