#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PATCH_FILE="$REPO_ROOT/build/core/patches/trusted-forwarded-client-ip.patch"
XRAY_SRC="${1:-$REPO_ROOT/third_party/Xray-core}"

if [[ ! -d "$XRAY_SRC" ]]; then
    echo "Не найдена директория с исходниками Xray: $XRAY_SRC" >&2
    exit 1
fi
if [[ ! -f "$PATCH_FILE" ]]; then
    echo "Не найден patch для доверенных proxy-заголовков: $PATCH_FILE" >&2
    exit 1
fi

if git -C "$XRAY_SRC" apply --reverse --check "$PATCH_FILE" >/dev/null 2>&1; then
    echo "Patch доверенных proxy-заголовков уже применён: $XRAY_SRC"
    exit 0
fi

if ! git -C "$XRAY_SRC" apply --check "$PATCH_FILE" >/dev/null 2>&1; then
    echo "Patch не подходит к исходникам Xray в $XRAY_SRC; проверьте ревизию подмодуля." >&2
    exit 1
fi

git -C "$XRAY_SRC" apply "$PATCH_FILE"
echo "Применён patch доверенных proxy-заголовков: $XRAY_SRC"
