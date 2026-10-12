#!/usr/bin/env bash
set -euo pipefail

die() { printf '%s\n' "$*" >&2; exit 1; }

action="${1:-}"
version="${2:-}"
[[ "$action" == build || "$action" == publish ]] || die 'Usage: release.sh build|publish VERSION'
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][a-zA-Z0-9.-]+)?$ ]] || die 'Set VERSION to a release tag, e.g. make release-build VERSION=v0.1.0'

cd "$(git rev-parse --show-toplevel)"
[[ -z "$(git status --porcelain)" ]] || die 'Commit or stash changes before building or publishing a release.'
commit="$(git rev-parse HEAD)"
out="dist/$version"
platforms=(darwin_arm64 darwin_amd64 linux_arm64 linux_amd64)
archives=()
for platform in "${platforms[@]}"; do
  archives+=("kango_${version}_${platform}.tar.gz")
done

if command -v sha256sum >/dev/null; then
  checksum=(sha256sum)
elif command -v shasum >/dev/null; then
  checksum=(shasum -a 256)
else
  die 'Install sha256sum or shasum to create and verify release checksums.'
fi

if [[ "$action" == build ]]; then
  go test ./...
  mkdir -p "$out"
  for platform in "${platforms[@]}"; do
    os="${platform%_*}"
    arch="${platform#*_}"
    staging="$out/package/$platform"
    mkdir -p "$staging"
    printf 'Building %s/%s\n' "$os" "$arch"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "$staging/kango" .
    cp README.md "$staging/README.md"
    cp web/static/LICENSES.txt "$staging/LICENSES.txt"
    COPYFILE_DISABLE=1 tar -czf "$out/kango_${version}_${platform}.tar.gz" -C "$staging" kango README.md LICENSES.txt
  done
  printf '%s\n' "$commit" > "$out/SOURCE_COMMIT"
  (cd "$out" && "${checksum[@]}" "${archives[@]}" SOURCE_COMMIT) > "$out/checksums.txt"
  printf 'Built release artifacts in %s/\n' "$out"
  exit 0
fi

command -v gh >/dev/null || die 'Install GitHub CLI (gh) to publish releases.'
[[ -f "$out/SOURCE_COMMIT" && -f "$out/checksums.txt" ]] || die "Run make release-build VERSION=$version first."
[[ "$(cat "$out/SOURCE_COMMIT")" == "$commit" ]] || die 'Artifacts were built from a different commit. Rebuild this version.'
for archive in "${archives[@]}"; do
  [[ -f "$out/$archive" ]] || die "Missing $archive. Rebuild this version."
done
(cd "$out" && "${checksum[@]}" -c checksums.txt)

repo="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"
remote_commit="$(gh api "repos/$repo/commits/$commit" --jq .sha)"
[[ "$remote_commit" == "$commit" ]] || die 'Push the source commit to GitHub before publishing.'
existing_tag="$(git ls-remote --tags origin "refs/tags/$version" "refs/tags/$version^{}")"
[[ -z "$existing_tag" ]] || die "Tag $version already exists. Choose a new VERSION."

cat > "$out/release-notes.md" <<EOF
Kango $version — local kanban with Go, HTMX, SQLite, and a generic MCP endpoint.

Download the archive matching your OS and architecture:
- macOS Apple Silicon: kango_${version}_darwin_arm64.tar.gz
- macOS Intel: kango_${version}_darwin_amd64.tar.gz
- Linux ARM64: kango_${version}_linux_arm64.tar.gz
- Linux AMD64: kango_${version}_linux_amd64.tar.gz

Extract the archive and run ./kango. Open http://localhost:2207.
MCP endpoint: http://127.0.0.1:2207/mcp.

Assets and SQLite are included in the executable. Data is stored separately in data/kango.db.
Source commit: $commit. SHA-256 checksums are attached.
EOF

uploads=()
for archive in "${archives[@]}"; do uploads+=("$out/$archive"); done
uploads+=("$out/checksums.txt" "$out/SOURCE_COMMIT")
flags=()
if [[ "$version" == *-* ]]; then flags+=(--prerelease); fi
gh release create "$version" "${uploads[@]}" --repo "$repo" --target "$commit" --title "$version" --notes-file "$out/release-notes.md" "${flags[@]}"
