#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
XRAY_SRC="${XRAY_SRC:-$REPO_ROOT/third_party/Xray-core}"
SOURCE_PATCH="$REPO_ROOT/build/core/patches/trusted-forwarded-client-ip.patch"
TEST_PATCH="$REPO_ROOT/build/core/patches/trusted-forwarded-client-ip-tests.patch"

if [[ ! -f "$XRAY_SRC/go.mod" ]]; then
    echo "Не найдены исходники Xray: $XRAY_SRC (сначала инициализируйте подмодуль)" >&2
    exit 1
fi
for patch_file in "$SOURCE_PATCH" "$TEST_PATCH"; do
    if [[ ! -f "$patch_file" ]]; then
        echo "Не найден patch-файл: $patch_file" >&2
        exit 1
    fi
done

source_commit="$(git -C "$XRAY_SRC" rev-parse HEAD)"
test_tree="$(mktemp -d "${TMPDIR:-/tmp}/vpn-ui-xray-proxy-test.XXXXXX")"
cleanup() {
    git -C "$XRAY_SRC" worktree remove --force "$test_tree" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Тесты запускаются во временном worktree: локальная копия подмодуля не меняется.
git -C "$XRAY_SRC" worktree add --detach "$test_tree" "$source_commit" >/dev/null
bash "$REPO_ROOT/build/core/apply-xray-proxy-patch.sh" "$test_tree"
git -C "$test_tree" apply --check "$TEST_PATCH"
git -C "$test_tree" apply "$TEST_PATCH"

cd "$test_tree"
go test ./common/protocol/http ./infra/conf ./transport/internet/grpc/... ./transport/internet/httpupgrade ./transport/internet/splithttp ./transport/internet/websocket
