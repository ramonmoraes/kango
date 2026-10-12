.PHONY: run build test release-build release-publish

VERSION ?=

run:
	go run .
build:
	go build -o kango .
test:
	go test -race ./...

# Build macOS and Linux binaries for AMD64 and ARM64, with checksums.
release-build:
	bash scripts/release.sh build "$(VERSION)"

# Publish the artifacts from release-build to a new GitHub release.
release-publish:
	bash scripts/release.sh publish "$(VERSION)"
