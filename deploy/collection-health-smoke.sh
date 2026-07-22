#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# Collection/Health 的 canonical smoke 必须自建 fresh DB、启动真实 API/Worker/Vite、
# 执行 River scan 与浏览器断言；保留此入口仅用于兼容直接调用旧脚本的场景。
exec bash "${SCRIPT_DIR}/collection-health-browser-smoke.sh" "$@"
