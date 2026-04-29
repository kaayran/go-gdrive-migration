#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source "$SCRIPT_DIR/project-vars.sh"

TARGET="all"
VERSION="0.1.0"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -Target)
      TARGET="${2:-}"
      shift 2
      ;;
    -Version)
      VERSION="${2:-}"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1"
      echo "Usage: ./build.sh [-Target all|win|linux|mac|mac-arm] [-Version x.y.z]"
      exit 2
      ;;
  esac
done

case "$TARGET" in
  all|win|linux|mac|mac-arm) ;;
  *)
    echo "Invalid -Target: $TARGET"
    echo "Allowed: all, win, linux, mac, mac-arm"
    exit 2
    ;;
esac

LDFLAGS="-s -w -X main.version=${VERSION}"
DIST_DIR="${SCRIPT_DIR}/dist"
mkdir -p "$DIST_DIR"

build() {
  local os="$1"
  local arch="$2"
  local out="$3"

  echo "-> Building ${os}/${arch} -> ${out}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -ldflags "$LDFLAGS" -o "${DIST_DIR}/${out}" .
}

if [[ "$TARGET" == "all" || "$TARGET" == "win" ]]; then
  build "windows" "amd64" "${APP_NAME}.exe"
fi
if [[ "$TARGET" == "all" || "$TARGET" == "linux" ]]; then
  build "linux" "amd64" "${APP_NAME}-linux"
fi
if [[ "$TARGET" == "all" || "$TARGET" == "mac" ]]; then
  build "darwin" "amd64" "${APP_NAME}-mac-intel"
fi
if [[ "$TARGET" == "all" || "$TARGET" == "mac-arm" ]]; then
  build "darwin" "arm64" "${APP_NAME}-mac-arm64"
fi

echo
echo "Done. Binaries in: ${DIST_DIR}"
ls -lh "${DIST_DIR}"
