#!/usr/bin/env bash
# Cross-compile pion-ipc for all platforms and publish npm packages.
#
# Usage:
#   ./scripts/build-npm.sh <version> [--dry-run] [--no-publish] [--build-only]
#
# Examples:
#   ./scripts/build-npm.sh 0.1.0
#   ./scripts/build-npm.sh 0.1.0 --dry-run
#   ./scripts/build-npm.sh 0.1.0 --build-only    # build only, skip publish
#   ./scripts/build-npm.sh 0.1.0 --no-publish     # alias for --build-only

set -euo pipefail

VERSION="${1:?Usage: $0 <version> [--dry-run] [--build-only]}"
DRY_RUN=false
NO_PUBLISH=false

shift
for arg in "$@"; do
	case "$arg" in
		--dry-run) DRY_RUN=true ;;
		--no-publish|--build-only) NO_PUBLISH=true ;;
		*) echo "Unknown option: $arg" >&2; exit 1 ;;
	esac
done

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
NPM_DIR="$ROOT_DIR/npm"
NPM_REGISTRY="${NPM_REGISTRY:-https://registry.npmjs.org/}"

# Platform tuples: npm-name GOOS GOARCH binary-name
PLATFORMS=(
	"linux-x64     linux   amd64  pion-ipc"
	"linux-arm64   linux   arm64  pion-ipc"
	"darwin-x64    darwin  amd64  pion-ipc"
	"darwin-arm64  darwin  arm64  pion-ipc"
	"win32-x64     windows amd64  pion-ipc.exe"
)

# 收集所有包名用于后续同步
ALL_PKG_NAMES=()

echo "==> Building pion-ipc v${VERSION} for ${#PLATFORMS[@]} platforms"

# 凭据检查（非 dry-run 且需要发布时）
if [[ "$DRY_RUN" == "false" && "$NO_PUBLISH" == "false" ]]; then
	echo ""
	echo "[PRE] 校验 npm 凭据"
	npm whoami --registry="$NPM_REGISTRY" >/dev/null
	npm ping --registry="$NPM_REGISTRY" >/dev/null
	echo "[INFO] 凭据有效"
fi

echo ""

for entry in "${PLATFORMS[@]}"; do
	read -r npm_name goos goarch bin_name <<< "$entry"
	pkg_dir="$NPM_DIR/$npm_name"
	bin_dir="$pkg_dir/bin"
	full_pkg_name="@coclaw/pion-ipc-${npm_name}"
	ALL_PKG_NAMES+=("$full_pkg_name")

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
		echo "  [build-only] Skipping npm publish"
	elif [ "$DRY_RUN" = true ]; then
		echo "  [dry-run] Would run: npm publish --access public from $pkg_dir"
	else
		(cd "$pkg_dir" && npm publish --access public --registry="$NPM_REGISTRY")
		echo "  Published: ${full_pkg_name}@${VERSION}"
	fi
done

# 跳过后续步骤（dry-run 或 build-only 模式）
if [[ "$DRY_RUN" == "true" || "$NO_PUBLISH" == "true" ]]; then
	echo ""
	_mode=""
	[[ "$DRY_RUN" == "true" ]] && _mode="dry-run"
	[[ "$NO_PUBLISH" == "true" ]] && _mode="${_mode:+$_mode, }build-only"
	echo "==> Done ($_mode)."
	exit 0
fi

# 触发 npmmirror 同步
echo ""
echo "[POST] 触发 npmmirror 同步..."
for pkg in "${ALL_PKG_NAMES[@]}"; do
	curl -sSf -X PUT "https://registry-direct.npmmirror.com/${pkg}/sync" >/dev/null 2>&1 \
		&& echo "  [sync] $pkg" \
		|| echo "  [WARN] $pkg 同步触发失败"
done

# 轮询确认所有包发布生效
echo ""
echo "[POST] 确认发布生效 (timeout: 120s)"
INITIAL_DELAY=5
INTERVAL=5
TIMEOUT=120

sleep "$INITIAL_DELAY"

elapsed=0
declare -A npm_ok mirror_ok
for pkg in "${ALL_PKG_NAMES[@]}"; do
	npm_ok[$pkg]=false
	mirror_ok[$pkg]=false
done

while [[ $elapsed -lt $TIMEOUT ]]; do
	all_done=true
	for pkg in "${ALL_PKG_NAMES[@]}"; do
		if [[ "${npm_ok[$pkg]}" != "true" ]]; then
			v=$(npm view "$pkg" dist-tags.latest --registry="$NPM_REGISTRY" 2>/dev/null) || true
			if [[ "$v" == "$VERSION" ]]; then
				npm_ok[$pkg]=true
				echo "  [npm]       $pkg@$VERSION -- OK (${elapsed}s)"
			else
				all_done=false
			fi
		fi
		if [[ "${mirror_ok[$pkg]}" != "true" ]]; then
			v=$(npm view "$pkg" dist-tags.latest 2>/dev/null) || true
			if [[ "$v" == "$VERSION" ]]; then
				mirror_ok[$pkg]=true
				echo "  [npmmirror] $pkg@$VERSION -- OK (${elapsed}s)"
			else
				all_done=false
			fi
		fi
	done

	if [[ "$all_done" == "true" ]]; then
		echo ""
		echo "==> Done. All ${#PLATFORMS[@]} platform packages published and verified."
		exit 0
	fi

	sleep "$INTERVAL"
	elapsed=$((elapsed + INTERVAL))

	if (( elapsed % 15 == 0 )); then
		echo "  [WAIT] ${elapsed}s ..."
	fi
done

echo ""
echo "[TIMEOUT] ${TIMEOUT}s 超时，部分包可能未在镜像生效"
for pkg in "${ALL_PKG_NAMES[@]}"; do
	[[ "${npm_ok[$pkg]}" == "true" ]] || echo "  [npm]       $pkg -- FAIL"
	[[ "${mirror_ok[$pkg]}" == "true" ]] || echo "  [npmmirror] $pkg -- FAIL"
done
exit 1
