#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# 为久久派（LS2K0300 99PI）编译 U-Boot，产物用于 PMON 的 fload 烧写
#
# 用法（在 WSL Ubuntu 22.04/24.04 里跑）：
#   bash build-uboot.sh wifi     # WiFi 版
#   bash build-uboot.sh tf       # TF 卡版
#
# 产物：<BSP>/out/u-boot-with-spl.bin
# ---------------------------------------------------------------------------
set -euo pipefail

VARIANT="${1:-}"
case "$VARIANT" in
    wifi) DEFCONFIG=loongson_2k300_99pi_wifi_defconfig ;;
    tf)   DEFCONFIG=loongson_2k300_99pi_tf_defconfig   ;;
    *)
        echo "用法: bash build-uboot.sh {wifi|tf}" >&2
        exit 1
        ;;
esac

# 本脚本所在目录 = 2k0300/_工具
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BSP="$(cd "$HERE/.." && pwd)/03-BSP"
UBOOT="$BSP/u-boot"
OUT="$BSP/out"
CCPREFIX=/opt/loongarch64-linux-gnu-gcc13.3

echo "==> BSP 目录: $BSP"

# --- 1. 工具链 -------------------------------------------------------------
if [ ! -x "$CCPREFIX/bin/loongarch64-linux-gnu-gcc" ]; then
    TARBALL="$BSP/cross-toolchain/gcc-13/loongarch64-linux-gnu-gcc13.3.tar.xz"
    [ -f "$TARBALL" ] || { echo "找不到工具链包: $TARBALL" >&2; exit 1; }
    echo "==> 解压工具链到 /opt（需要 sudo）"
    ( cd "$(dirname "$TARBALL")" && md5sum -c "$(basename "$TARBALL").md5" )
    sudo tar xf "$TARBALL" -C /opt
fi
"$CCPREFIX/bin/loongarch64-linux-gnu-gcc" -v 2>&1 | tail -1

# --- 2. 依赖 ---------------------------------------------------------------
need_pkgs=()
for c in make gcc bison flex; do
    command -v "$c" >/dev/null 2>&1 || need_pkgs+=("$c")
done
[ -f /usr/include/openssl/ssl.h ] || need_pkgs+=("libssl-dev")
python3 -c "import libfdt" 2>/dev/null || need_pkgs+=("python3-dev" "swig")
if [ ${#need_pkgs[@]} -gt 0 ]; then
    echo "==> 安装依赖: ${need_pkgs[*]}"
    sudo apt-get update -qq
    sudo apt-get install -y -qq build-essential bison flex libssl-dev bc \
        python3-dev swig python3-setuptools
fi

# --- 3. 环境变量（等同 BSP 的 set_env.sh）----------------------------------
export PATH="$CCPREFIX/bin:$PATH"
export LD_LIBRARY_PATH="$CCPREFIX/lib:${LD_LIBRARY_PATH:-}"
export ARCH=loongarch
export CROSS_COMPILE=loongarch64-linux-gnu-

# --- 4. 编译 ---------------------------------------------------------------
cd "$UBOOT"
echo "==> 配置 $DEFCONFIG"
make distclean >/dev/null
make "$DEFCONFIG"

echo "==> 编译（$(nproc) 线程）"
make -j"$(nproc)"

# --- 5. 产物 ---------------------------------------------------------------
mkdir -p "$OUT"
cp -v u-boot-with-spl.bin "$OUT/"
[ -f u-boot.dtb ] && cp -v u-boot.dtb "$OUT/"

echo
echo "==================== 完成 ===================="
ls -lh "$OUT"
echo
sha256sum "$OUT/u-boot-with-spl.bin"
echo
echo "下一步：把 $OUT/u-boot-with-spl.bin 放到 TFTP 服务器目录，"
echo "      在 PMON 里执行：fload tftp://<你的PC IP>/u-boot-with-spl.bin"
echo "      等到 'Programming end!' 出现且回到 PMON> 才算烧完。"
