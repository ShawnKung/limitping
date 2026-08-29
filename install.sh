#!/bin/sh

set -eu

repository="ShawnKung/limitping"
install_dir="${HOME}/.local/bin"

fail() {
    printf 'limitping installer: %s\n' "$*" >&2
    exit 1
}

command -v curl >/dev/null 2>&1 || fail "需要 curl"
command -v tar >/dev/null 2>&1 || fail "需要 tar"

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
archive="limitping_${version}_${os}_${arch}.tar.gz"
download_base="https://github.com/${repository}/releases/download/${tag}"
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/limitping.XXXXXXXX")

cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup 0
trap 'exit 1' HUP INT TERM

printf '正在下载 limitping %s (%s/%s)...\n' "$version" "$os" "$arch"
curl -fsSL "${download_base}/${archive}" -o "${tmp_dir}/${archive}"
curl -fsSL "${download_base}/checksums.txt" -o "${tmp_dir}/checksums.txt"

expected=$(awk -v name="$archive" '$2 == name { print; exit }' "${tmp_dir}/checksums.txt")
[ -n "$expected" ] || fail "checksums.txt 中没有 ${archive}"
printf '%s\n' "$expected" >"${tmp_dir}/expected-checksum.txt"

if command -v sha256sum >/dev/null 2>&1; then
    (cd "$tmp_dir" && sha256sum -c expected-checksum.txt >/dev/null)
elif command -v shasum >/dev/null 2>&1; then
    (cd "$tmp_dir" && shasum -a 256 -c expected-checksum.txt >/dev/null)
else
    fail "需要 sha256sum 或 shasum 校验下载文件"
fi

tar -xzf "${tmp_dir}/${archive}" -C "$tmp_dir"
binary="${tmp_dir}/limitping_${version}_${os}_${arch}/limitping"
[ -f "$binary" ] || fail "发布包中没有 limitping 二进制"

mkdir -p "$install_dir"
install -m 0755 "$binary" "${install_dir}/limitping"
chmod 0755 "${install_dir}/limitping"

printf '已安装: %s\n' "${install_dir}/limitping"
"${install_dir}/limitping" version

case ":${PATH}:" in
    *":${install_dir}:"*) ;;
    *) printf '提示: 请将 %s 加入 PATH。\n' "$install_dir" ;;
esac
