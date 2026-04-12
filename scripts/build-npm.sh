#!/usr/bin/env bash
# Cross-compile pion-ipc for all platforms and publish npm packages.
#
# Usage:
#   ./scripts/build-npm.sh <version> [--dry-run] [--no-publish]
#
# Examples:
#   ./scripts/build-npm.sh 0.1.0
#   ./scripts/build-npm.sh 0.1.0 --dry-run
#   ./scripts/build-npm.sh 0.1.0 --no-publish   # build only, skip npm publish

set -euo pipefail

VERSION="${1:?Usage: $0 <version> [--dry-run] [--no-publish]}"
DRY_RUN=false
NO_PUBLISH=false

shift
for arg in "$@"; do
	case "$arg" in
		--dry-run) DRY_RUN=true ;;
		--no-publish) NO_PUBLISH=true ;;
		*) echo "Unknown option: $arg" >&2; exit 1 ;;
	esac
done

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
NPM_DIR="$ROOT_DIR/npm"

# Platform tuples: npm-name GOOS GOARCH binary-name
PLATFORMS=(
	"linux-x64     linux   amd64  pion-ipc"
	"linux-arm64   linux   arm64  pion-ipc"
	"darwin-x64    darwin  amd64  pion-ipc"
	"darwin-arm64  darwin  arm64  pion-ipc"
	"win32-x64     windows amd64  pion-ipc.exe"
)

echo "==> Building pion-ipc v${VERSION} for ${#PLATFORMS[@]} platforms"

for entry in "${PLATFORMS[@]}"; do
	read -r npm_name goos goarch bin_name <<< "$entry"
	pkg_dir="$NPM_DIR/$npm_name"
	bin_dir="$pkg_dir/bin"

	echo "--- ${npm_name} (GOOS=${goos} GOARCH=${goarch})"

	# Update version in package.json
	if command -v jq &>/dev/null; then
		jq --arg v "$VERSION" '.version = $v' "$pkg_dir/package.json" > "$pkg_dir/package.json.tmp"
		mv "$pkg_dir/package.json.tmp" "$pkg_dir/package.json"
	else
		sed -i "s/\"version\": \".*\"/\"version\": \"${VERSION}\"/" "$pkg_dir/package.json"
	fi

	# Cross-compile
	mkdir -p "$bin_dir"
	if [ "$DRY_RUN" = true ]; then
		echo "  [dry-run] Would build: CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -o $bin_dir/$bin_name"
	else
		CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
			go build -ldflags="-s -w" -o "$bin_dir/$bin_name" ./cmd/pion-ipc
		echo "  Built: $bin_dir/$bin_name ($(du -h "$bin_dir/$bin_name" | cut -f1))"
	fi

	# Publish
	if [ "$NO_PUBLISH" = true ]; then
		echo "  [no-publish] Skipping npm publish"
	elif [ "$DRY_RUN" = true ]; then
		echo "  [dry-run] Would run: npm publish --access public from $pkg_dir"
	else
		(cd "$pkg_dir" && npm publish --access public)
		echo "  Published: @coclaw/pion-ipc-${npm_name}@${VERSION}"
	fi
done

echo "==> Done. Published ${#PLATFORMS[@]} platform packages."
