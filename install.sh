#!/bin/sh

set -eu

repository="ShawnKung/limitping"
install_dir="${HOME}/.local/bin"

fail() {
    printf 'limitping installer: %s\n' "$*" >&2
    exit 1
}

command -v curl >/dev/null 2>&1 || fail "需要 curl"

case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux) os="linux" ;;
    *) fail "不支持的操作系统: $(uname -s)" ;;
esac

case "$(uname -m)" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) fail "不支持的 CPU 架构: $(uname -m)" ;;
esac

latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
    "https://github.com/${repository}/releases/latest")
tag=${latest_url##*/}
case "$tag" in
    v*) ;;
    *) fail "无法确定最新版本" ;;
esac

version=${tag#v}
binary_name="limitping_${version}_${os}_${arch}"
download_base="https://github.com/${repository}/releases/download/${tag}"
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/limitping.XXXXXXXX")

cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup 0
trap 'exit 1' HUP INT TERM

printf '正在下载 limitping %s (%s/%s)...\n' "$version" "$os" "$arch"
curl -fsSL "${download_base}/checksums.txt" -o "${tmp_dir}/checksums.txt"
curl -fsSL "${download_base}/${binary_name}" -o "${tmp_dir}/${binary_name}"

expected=$(awk -v name="$binary_name" '$2 == name { print; exit }' "${tmp_dir}/checksums.txt")
[ -n "$expected" ] || fail "checksums.txt 中没有 ${binary_name}"
printf '%s\n' "$expected" >"${tmp_dir}/expected-checksum.txt"

if command -v sha256sum >/dev/null 2>&1; then
    (cd "$tmp_dir" && sha256sum -c expected-checksum.txt >/dev/null)
elif command -v shasum >/dev/null 2>&1; then
    (cd "$tmp_dir" && shasum -a 256 -c expected-checksum.txt >/dev/null)
else
    fail "需要 sha256sum 或 shasum 校验下载文件"
fi

binary="${tmp_dir}/${binary_name}"
[ -f "$binary" ] || fail "Release 中没有 ${binary_name}"

mkdir -p "$install_dir"
install -m 0755 "$binary" "${install_dir}/limitping"
chmod 0755 "${install_dir}/limitping"

printf '已安装: %s\n' "${install_dir}/limitping"
"${install_dir}/limitping" version

case ":${PATH}:" in
    *":${install_dir}:"*) ;;
    *) printf '提示: 请将 %s 加入 PATH。\n' "$install_dir" ;;
esac
