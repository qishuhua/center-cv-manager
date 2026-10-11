.DEFAULT_GOAL := build
.PHONY: web build up down logs dev test clean

web:
	cd web && npm ci && npm run build

build:
	docker compose build

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f --tail=100

dev:
	go run ./cmd/server --listen 127.0.0.1:8099 --data ./data

test:
	go test ./...
	cd web && npm ci && npm run build

clean:
	docker compose down --rmi local
