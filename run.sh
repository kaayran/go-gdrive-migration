#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
source "$SCRIPT_DIR/project-vars.sh"

if [[ ! -f "./dist/${APP_EXE}" ]]; then
  echo "[ERROR] Binary not found: ./dist/${APP_EXE}"
  echo "Build first: ./build.sh -Target linux (or -Target mac / -Target mac-arm)"
  exit 1
fi

echo "Running ${APP_NAME}..."
echo
"./dist/${APP_EXE}" --config config.yaml "$@"
