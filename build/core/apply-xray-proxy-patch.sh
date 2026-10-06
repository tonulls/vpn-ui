#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PATCH_FILES=(
    "$REPO_ROOT/build/core/patches/trusted-forwarded-client-ip.patch"
    "$REPO_ROOT/build/core/patches/inbound-user-traffic-stats.patch"
)
XRAY_SRC="${1:-$REPO_ROOT/third_party/Xray-core}"

if [[ ! -d "$XRAY_SRC" ]]; then
    echo "Не найдена директория с исходниками Xray: $XRAY_SRC" >&2
    exit 1
fi

for patch_file in "${PATCH_FILES[@]}"; do
    if [[ ! -f "$patch_file" ]]; then
        echo "Не найден patch Xray: $patch_file" >&2
        exit 1
    fi

    patch_name="$(basename "$patch_file")"
    if git -C "$XRAY_SRC" apply --reverse --check "$patch_file" >/dev/null 2>&1; then
        echo "Patch уже применён: $patch_name"
        continue
    fi

    if ! git -C "$XRAY_SRC" apply --check "$patch_file" >/dev/null 2>&1; then
        echo "Patch $patch_name не подходит к исходникам Xray в $XRAY_SRC; проверьте ревизию." >&2
        exit 1
    fi

    git -C "$XRAY_SRC" apply "$patch_file"
    echo "Применён patch $patch_name"
done
