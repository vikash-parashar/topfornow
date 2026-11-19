.PHONY: run test build docker
run:
	go run ./cmd/topfornow serve
test:
	go vet ./... && go test ./...
# Full acceptance test needs a throwaway Postgres database:
#   TEST_DATABASE_URL=postgres://.../topfornow_test?sslmode=disable make test
build:
	CGO_ENABLED=0 go build -o bin/topfornow ./cmd/topfornow
docker:
	docker build -t topfornow .
