#!/usr/bin/env bash
# Multica fork CLI one-click installer (macOS / Linux).
# Served from this deployment's own /downloads/ channel; installs the fork
# build (commit b2ebe15ec) and points future updates at the fork release repo.
# Run:  curl -fsSL http://8.148.26.166:2080/downloads/install.sh | bash
# (Regenerate via scripts/publish-cli-downloads.sh, which bakes http://8.148.26.166:2080/downloads.)

set -euo pipefail

BASE='http://8.148.26.166:2080/downloads'
UPDATE_REPO='Siyue-on-my-way/multica'
MIN_COMMIT='b2ebe15ec'

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"; [ "$OS" = "darwin" ] && OS=darwin || OS=linux
ARCH="$(uname -m)"; [ "$ARCH" = "aarch64" ] && ARCH=arm64
ASSET="multica-${OS}-${ARCH}"
[ "$OS" = "darwin" ] && [ "$ARCH" != "arm64" ] && { echo "仅提供 Apple Silicon (arm64) 构建"; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "1/5 downloading $ASSET ..."
curl -fsSL "$BASE/$ASSET" -o "$TMP/$ASSET"

echo "2/5 verifying SHA-256 ..."
curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums"
want="$(grep "$ASSET" "$TMP/checksums" | awk '{print $1}')"
have="$(sha256sum "$TMP/$ASSET" | awk '{print $1}')"
[ "$(uname -s)" = "Darwin" ] && have="$(shasum -a 256 "$TMP/$ASSET" | awk '{print $1}')"
if [ "$have" != "$want" ]; then echo "SHA-256 mismatch: got $have want $want"; exit 1; fi
echo "   OK ($have)"

echo "3/5 stopping old daemon and installing ..."
multica daemon stop 2>/dev/null || true
sleep 1
if [ -w /usr/local/bin ] || [ "$(id -u)" = "0" ]; then
    DST=/usr/local/bin/multica
    install -m 0755 "$TMP/$ASSET" "$DST"
elif command -v sudo >/dev/null 2>&1; then
    DST=/usr/local/bin/multica
    sudo install -m 0755 "$TMP/$ASSET" "$DST"
else
    DST="$HOME/.local/bin/multica"
    mkdir -p "$(dirname "$DST")"
    install -m 0755 "$TMP/$ASSET" "$DST"
    case ":$PATH:" in *":$(dirname "$DST"):"*) ;; *)
        echo "注意: $(dirname "$DST") 不在 PATH 中，请把它加入 ~/.bashrc 或 ~/.zshrc" ;;
    esac
fi

echo "4/5 setting MULTICA_UPDATE_REPO ..."
line="export MULTICA_UPDATE_REPO='$UPDATE_REPO'"
for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
    [ -f "$rc" ] && ! grep -q 'MULTICA_UPDATE_REPO' "$rc" && echo "$line" >> "$rc" || true
done
export MULTICA_UPDATE_REPO="$UPDATE_REPO"

echo "5/5 verifying and starting daemon ..."
"$DST" version
"$DST" daemon start

echo
echo "✅ 完成。multica version 显示的 commit 必须与 manifest.json 中的 commit 一致（当前发布见 $BASE/manifest.json）。"
