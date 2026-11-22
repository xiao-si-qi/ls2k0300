# 久久派：安装 Alpine Linux 3.21（新世界）

> 2026-11-21 实测完成。板子：LS2K300-99PI-WIFI（512 MB / 8 GB eMMC）
> 结果：eMMC 启动 Alpine 3.21 + Linux 6.9.0-rc7，SSH 可用，apk 可用（23,711 包）

---

## 1. 最终状态

| 项 | 值 |
|---|---|
| 系统 | **Alpine Linux 3.21.0**（loongarch64） |
| 内核 | 6.9.0-rc7（官方 BSP，内置设备树） |
| 引导 | SPI NOR 官方 U-Boot 2024.04（`u-boot-spl-gz.bin`，580,432 B） |
| 根文件系统 | eMMC `/dev/mmcblk0p1`，ext4，7.1 GB |
| 内存 | 432 MB 可用 |
| 网络 | eth0 DHCP（千兆） |
| SSH | `root` / `root`，`PermitRootLogin yes` |
| 包管理 | `apk`，源为 `dl-cdn.alpinelinux.org/alpine/v3.21` |

SSH 连接：

```bash
ssh root@<板子IP>      # 密码 root
```

---

## 2. 为什么换 Alpine

原来装的官方 buildroot 是极致精简版：**没有 SSH、没有包管理器、没有 WiFi 工具、连 `mkfs.ext4` 都没有**。
Alpine 的 minirootfs 只有 3.3 MB，但带完整 `apk`，一条命令就能补齐任何东西：

```sh
apk add openssh              # SSH
apk add build-base linux-headers  # 直接在板子上编译内核模块
apk add wpa_supplicant iw    # WiFi 工具
apk add e2fsprogs            # mkfs.ext4 等
```

而且 Alpine 3.21 本来就在官方 BSP 2.0 的支持列表里。

---

## 3. 安装步骤（可复现）

### 3.1 准备

- U 盘/网线不需要，全程用 **TFTP + 串口**
- 需要的文件放在 TFTP 根目录（`03-BSP/out/`）：
  - `uImage-wifi`（内核 8.7 MB）
  - `initrd_la.uimg`（buildroot 的 initramfs，27 MB，用于进入"安装环境"）
  - `alpine.tar.gz`（Alpine minirootfs，3.3 MB）

### 3.2 进 U-Boot，启动 initramfs

板子 0 秒自动引导，需在启动瞬间**狂按 `c`** 抢进 `SOC#`：

```sh
SOC# setenv ipaddr 192.168.137.50
SOC# setenv netmask 255.255.255.0
SOC# setenv serverip 192.168.137.1
SOC# setenv tftpblocksize 1468
SOC# tftpboot 0x9000000003000000 uImage-wifi
SOC# tftpboot 0x900000000a000000 initrd_la.uimg
SOC# setenv bootargs console=ttyS0,115200 rdinit=/sbin/init
SOC# bootm 0x9000000003000000 0x900000000a000000
```

进 initramfs 后登录 `root`（空密码）。

### 3.3 写 Alpine 到 eMMC

```sh
ip link set end0 up
udhcpc -i end0 -q -n
tftp -g -r alpine.tar.gz -l /tmp/alpine.tar.gz 192.168.137.1

mkdir -p /mnt
mount /dev/mmcblk0p1 /mnt
cd / && rm -rf /mnt/*
cd /mnt && gzip -dc /tmp/alpine.tar.gz | tar -xf -
```

> ⚠ 这里的 busybox `tar` **不支持 `-z`**，必须用 `gzip -dc | tar -xf -` 管道。

### 3.4 配置（关键，否则起不来）

Alpine minirootfs **不含 openrc**（没有 `/sbin/openrc`、`/etc/init.d`、`/etc/runlevels`），
所以第一次必须用 busybox init 引导，进去后再 `apk add openrc`。

`/mnt/etc/inittab`（覆盖）：

```
::sysinit:/bin/mount -t proc proc /proc
::sysinit:/bin/mount -t sysfs sys /sys
::sysinit:/bin/mount -t devtmpfs dev /dev
::sysinit:/bin/mount -t tmpfs tmpfs /tmp
::respawn:/sbin/getty -L 115200 ttyS0 vt100
::ctrlaltdel:/sbin/reboot
::shutdown:/bin/umount -a -r
```

`/mnt/etc/fstab`：

```
/dev/mmcblk0p1 / ext4 rw,relatime 0 1
proc /proc proc defaults 0 0
tmpfs /tmp tmpfs defaults 0 0
```

**root 密码必须先解锁**（minirootfs 里 root 是 `root:*` 锁定状态）：

```sh
sed -i 's/^root:\*:/root::/' /mnt/etc/shadow
```

### 3.5 首次启动 Alpine

```sh
sync; umount /mnt; reboot
```

U-Boot 默认 `bootcmd` 会从 `ext4load mmc 0:1 ${loadaddr} /boot/uImage` 加载内核，
但 eMMC 被清空后 **`/boot/uImage` 没了**，所以第一次要手动从 TFTP 启动：

```sh
SOC# tftpboot 0x9000000003000000 uImage
SOC# setenv bootargs console=ttyS0,115200 root=/dev/mmcblk0p1 rootfstype=ext4 rw rootwait init=/sbin/init
SOC# bootm 0x9000000003000000
```

进了 Alpine 后，用 **HTTP**（busybox 没有 tftp 客户端）把内核补回去：

```sh
# PC 上：cd 03-BSP/out && python -m http.server 8000
wget -q -O /boot/uImage http://192.168.137.1:8000/uImage
```

之后就能自动从 eMMC 引导了。

### 3.6 装 openrc 与基础服务

```sh
apk update
apk add openrc openssh e2fsprogs curl htop nano
```

换成 openrc 的 inittab：

```
::sysinit:/sbin/openrc sysinit
::sysinit:/sbin/openrc boot
::wait:/sbin/openrc default
tty1::respawn:/sbin/getty 38400 tty1
ttyS0::respawn:/sbin/getty -L 115200 ttyS0 vt100
::ctrlaltdel:/sbin/reboot
::shutdown:/sbin/openrc shutdown
```

配网络 `/etc/network/interfaces`：

```
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp
```

把服务放进 runlevel：

```sh
rc-update add devfs sysinit;      rc-update add sysfs sysinit
rc-update add procfs sysinit;     rc-update add dmesg sysinit
rc-update add hostname boot;      rc-update add hwclock boot
rc-update add modules boot;       rc-update add sysctl boot
rc-update add bootmisc boot;      rc-update add fsck boot
rc-update add root boot;          rc-update add networking boot
rc-update add local default;      rc-update add sshd default
rc-update add mount-ro shutdown;  rc-update add killprocs shutdown
rc-update add savecache shutdown
```

---

## 4. 踩过的坑（都是真机实测）

| 现象 | 原因 | 解决 |
|---|---|---|
| `tar -xzf` 打印 usage | buildroot 的 busybox tar 不支持 `-z` | `gzip -dc x.tar.gz \| tar -xf -` |
| 执行完 `chpasswd` 后**怎么都登不进去** | minirootfs 的 shadow 是锁定态，chpasswd 写出的哈希登录程序不认 | 离线用 `mkpasswd -m sha512` 生成哈希直接写 `/etc/shadow` |
| `init=/bin/sh` 启动后**整机静默** | shell 太早启动，`/dev/console` 未就绪，拿不到 stdio | 不要用；改用 initramfs 做离线修改 |
| U-Boot 抢不到，直接进系统 | 自动引导是 0 秒 | 后台持续发 `c`，再按复位 |
| 清空 eMMC 后 U-Boot 停在 `SOC#` | `/boot/uImage` 被删了 | TFTP 手动 `bootm`，进系统后再补回 |
| `wget` 找不到 | Alpine busybox **没有 tftp**，但有 wget | PC 起 `python -m http.server` |
| SSH 密码登录被拒 | Alpine sshd 默认 `PermitRootLogin prohibit-password` | 改 `PermitRootLogin yes` 并重启 sshd |
| 重启后 IP 变了 | ICS 的 DHCP 每次分配不同地址 | 看串口或 `arp -a` 找 MAC `64:48:48:48:48:60` |

---

## 5. 已知缺口

- **WiFi 没有驱动**：板载 AIC8800（SDIO）。内核树里无此驱动（外部驱动），
  手上的 `.ko` 是 4.19 版本，vermagic 不匹配。需要在板子上编译：
  ```sh
  apk add build-base linux-headers git
  # 拉 AIC8800 for Linux 6.x 源码，ARCH 改为 loongarch 编译
  ```
- **没有 `mkfs` 之外的存储工具**：已装 e2fsprogs，可用。
- **IP 不固定**：建议在 `/etc/network/interfaces` 里改静态地址，或用主机名+`avahi`。

---

## 6. 回滚路径

想退回官方 buildroot：TFTP 取 `rootfs.tar`，在 initramfs 里清空 eMMC 后解压即可（流程同上）。

想退回旧世界（LoongOS）：用 `07-官方镜像/PMON回滚/PMON_99PAI_V1.2_官方.bin` 刷回 SPI
（`sf probe; sf erase 0 0xE7000; sf write 0x3000000 0 0xC1B05`，大小按实际文件算），
再装 `红WiFi固件系统.zip` 里的 LoongOS 根文件系统。
