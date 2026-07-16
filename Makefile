SHELL := /bin/sh

.PHONY: test go-test go-vet web-install web-lint web-typecheck web-test web-build openapi-check compose-check docker-build compose-up compose-down

test: go-test go-vet web-lint web-typecheck web-test web-build openapi-check compose-check

go-test:
	go test ./cmd/... ./internal/...

go-vet:
	go vet ./cmd/... ./internal/...

web-install:
	npm ci --prefix web

web-lint:
	npm run lint --prefix web

web-typecheck:
	npm run typecheck --prefix web

web-test:
	npm run test --prefix web

web-build:
	npm run build --prefix web

openapi-check:
	node api/openapi/check.mjs

compose-check:
	docker compose -f deploy/compose.yml --env-file .env.example config --quiet

docker-build:
	docker build -f deploy/Dockerfile -t zhixu:local .

compose-up:
	docker compose -f deploy/compose.yml --env-file .env.example up -d --build --wait

compose-down:
	docker compose -f deploy/compose.yml --env-file .env.example down -v
