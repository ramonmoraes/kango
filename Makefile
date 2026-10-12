.PHONY: run build test
run:
	go run .
build:
	go build -o kango .
test:
	go test -race ./...
