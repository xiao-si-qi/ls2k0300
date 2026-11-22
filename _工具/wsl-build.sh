#!/bin/bash
# 在 WSL 里以 root 运行：原生 clone U-Boot（保留符号链接、无 CRLF）并编译
# 用法: bash wsl-build3.sh [wifi|tf]
set -uo pipefail
exec 2>&1

VARIANT="${1:-wifi}"
if [ "$VARIANT" = "tf" ]; then
    DEFCONFIG=loongson_2k300_99pi_tf_defconfig
    BOARD_TAG=tf
else
    DEFCONFIG=loongson_2k300_99pi_wifi_defconfig
    BOARD_TAG=wifi
fi

WORK=/root/uboot-native
OUT_WIN=/mnt/c/Users/xiaosiqi/Documents/esp32/2k0300/03-BSP/out
TC=/opt/loongarch64-linux-gnu-gcc13.3
REPO=https://gitee.com/open-loongarch/u-boot.git

echo "=== [1/5] 确保 git 存在 ==="
if ! command -v git >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get install -y -qq git || { echo "GIT_INSTALL_FAILED"; exit 10; }
fi
git --version

echo "=== [2/5] 原生 clone（Linux 文件系统，符号链接/换行符正确）==="
if [ -d "$WORK/.git" ]; then
    echo "复用已有 clone"
    git -C "$WORK" fetch --depth 1 origin && git -C "$WORK" reset --hard FETCH_HEAD
else
    rm -rf "$WORK"
    git clone --depth 1 --single-branch "$REPO" "$WORK" || { echo "CLONE_FAILED"; exit 11; }
fi
cd "$WORK" || exit 12
echo "HEAD: $(git log -1 --format='%h %s' 2>/dev/null)"
echo "符号链接数: $(find . -type l | wc -l)"
echo "ctype.h 指向: $(readlink include/ctype.h 2>/dev/null || echo '不是符号链接(异常)')"

echo "=== [3/5] 检查工具链 ==="
[ -x "$TC/bin/loongarch64-linux-gnu-gcc" ] || { echo "TOOLCHAIN_MISSING"; exit 13; }
"$TC/bin/loongarch64-linux-gnu-gcc" -dumpversion

echo "=== [4/5] 配置 $DEFCONFIG ==="
export PATH="$TC/bin:$PATH"
export LD_LIBRARY_PATH="$TC/lib:$TC/loongarch64-linux-gnu/lib64:${LD_LIBRARY_PATH:-}"
export ARCH=loongarch
export CROSS_COMPILE=loongarch64-linux-gnu-
if ! make "$DEFCONFIG" > /tmp/defconfig.log 2>&1; then
    echo "DEFCONFIG_FAILED"; tail -30 /tmp/defconfig.log; exit 15
fi
grep -E '^CONFIG_(TARGET_LS2K300|SPL|CPU_1000MHZ)=' .config

echo "=== [5/5] 编译 ==="
if ! make -j"$(nproc)" > /tmp/build.log 2>&1; then
    echo "BUILD_FAILED"
    grep -nE ': error:|Error [0-9]' /tmp/build.log | head -20
    echo "---- 末尾 ----"; tail -30 /tmp/build.log
    exit 16
fi
tail -8 /tmp/build.log

[ -f u-boot-with-spl.bin ] || { echo "NO_ARTIFACT"; ls -l u-boot*; exit 17; }

mkdir -p "$OUT_WIN"
cp u-boot-with-spl.bin "$OUT_WIN/u-boot-with-spl-$BOARD_TAG.bin"
[ -f u-boot.dtb ] && cp u-boot.dtb "$OUT_WIN/u-boot-$BOARD_TAG.dtb"

echo
echo "==================== DONE ===================="
ls -l "$OUT_WIN"
sha256sum "$OUT_WIN/u-boot-with-spl-$BOARD_TAG.bin"
echo "BUILD_OK"
