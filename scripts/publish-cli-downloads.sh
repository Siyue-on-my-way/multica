#!/usr/bin/env bash
# publish-cli-downloads.sh — rebuild the fork CLI for every supported platform
# and publish it to the self-hosted /downloads/ channel (nginx static files).
#
# This is the release valve for private-fork distribution: upstream GitHub
# releases can never carry fork code (the CLI updater points at the official
# repo), so this deployment serves its own binaries. Run it from the repo
# root after every server-side change that touches the CLI/daemon, so the
# channel never ships stale artifacts (SIY-167 rollout lesson).
#
# Usage:
#   scripts/publish-cli-downloads.sh                    # build from HEAD
#   DOWNLOAD_BASE=http://10.0.0.2:2080/downloads \
#     scripts/publish-cli-downloads.sh                  # bake another base
#
# Committed artifacts: install.sh / install.ps1 / checksums.txt / manifest.json
# and the binaries themselves. Binaries are reproducible from the checkout,
# so they are gitignored; everything else must stay in git.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="$REPO_ROOT/docker/nginx/downloads"
VERSION="$(git -C "$REPO_ROOT" describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD)"
DATE="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE"
DOWNLOAD_BASE="${DOWNLOAD_BASE:-http://8.148.26.166:2080/downloads}"

command -v go >/dev/null 2>&1 || { echo "go not found in PATH" >&2; exit 1; }

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

build() {
    local goos="$1" goarch="$2" out="$3" exe=""
    [ "$goos" = "windows" ] && exe=".exe"
    echo "[build] $goos/$goarch -> $out"
    (cd "$REPO_ROOT/server" && GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
        go build -ldflags "$LDFLAGS" -o "$STAGE/$out" ./cmd/multica)
}

build linux  amd64 multica-linux-amd64
build darwin arm64 multica-darwin-arm64
build windows amd64 multica-windows-amd64.exe

(
    cd "$STAGE"
    sha256sum multica-linux-amd64 multica-darwin-arm64 multica-windows-amd64.exe \
        > checksums.txt
    # sha256sum on macOS is not guaranteed; normalize through the local tool.
    if ! sha256sum -c checksums.txt >/dev/null 2>&1 && command -v shasum >/dev/null 2>&1; then
        shasum -a 256 multica-linux-amd64 multica-darwin-arm64 multica-windows-amd64.exe \
            > checksums.txt
    fi
    python3 - "$VERSION" "$COMMIT" "$DATE" > manifest.json <<'PY'
import json, sys
version, commit, date = sys.argv[1:4]
print(json.dumps({
    "channel": "siyue-fork",
    "version": version,
    "commit": commit,
    "built_at": date,
    "platforms": {
        "linux-amd64": "multica-linux-amd64",
        "darwin-arm64": "multica-darwin-arm64",
        "windows-amd64": "multica-windows-amd64.exe",
    },
}, indent=2))
PY

    # Bake this deployment's download base into the served installers.
    sed "s#__DOWNLOAD_BASE__#${DOWNLOAD_BASE}#g" \
        "$REPO_ROOT/scripts/install-selfhost.ps1" > install.ps1
    sed "s#__DOWNLOAD_BASE__#${DOWNLOAD_BASE}#g" \
        "$REPO_ROOT/scripts/install-selfhost.sh" > install.sh
    chmod +x install.sh
)

mkdir -p "$OUT_DIR"
cp "$STAGE"/multica-* "$STAGE/checksums.txt" "$STAGE/manifest.json" \
   "$STAGE/install.ps1" "$STAGE/install.sh" "$OUT_DIR/"

echo
echo "Published to $OUT_DIR:"
ls -la "$OUT_DIR"
echo
echo "Commit: $COMMIT ($VERSION)"
echo "Verify: curl -fsSL ${DOWNLOAD_BASE}/manifest.json"
echo "Install (Windows): irm ${DOWNLOAD_BASE%/downloads}/downloads/install.ps1 | iex"
echo "Install (mac/linux): curl -fsSL ${DOWNLOAD_BASE%/downloads}/downloads/install.sh | bash"
