# U-Boot 救援与踩坑教训

> 板子改坏了起不来怎么办。这份文档是在**多次把板子搞砖**之后总结的救援路径，
> 以及那些用一次错误换来一条的教训。

---

## 一、先判断卡在哪一层

串口（115200 8N1）是唯一可靠的诊断手段。看输出停在哪：

| 现象 | 说明 | 去第几节 |
|---|---|---|
| **完全无输出** | 供电、串口接线（TX/RX 必须交叉）、或 SoC 没起来 | 先查硬件 |
| 能看到 U-Boot 但打出 `TLB exception!` | U-Boot 本身有问题（多半是自编的） | §2.4 |
| U-Boot 正常，`bootm` 后卡在某个时间点 | **内核或根文件系统问题**（最常见） | §2.1 / §2.2 |
| 内核起来了但 `init` 失败 | 根文件系统挂载失败 | §2.2 |
| 能空跑但要很久 | ext4 日志回放中，等一会儿或修复 | §2.2 |

---

## 二、三条救援路径

### 2.1 用备份内核启动（最常用，也最快）

板子起不来但 eMMC 上的系统还在时，用 U-Boot 直接加载备份内核，**不落盘**：

```sh
# 上电瞬间狂按 'c' 进 U-Boot（这块板 autoboot 是 0 秒，见 §3.2）
SOC# ext4load mmc 0:1 0x9000000003000000 /boot/uImage.bak-6.9rc7
SOC# setenv bootargs console=ttyS0,115200 root=/dev/mmcblk0p1 rootfstype=ext4 rw rootwait init=/sbin/init
SOC# bootm 0x9000000003000000
```

启动成功、SSH 能进之后，再把备份内核换回去：

```sh
cd /boot && cp uImage.bak-6.9rc7 uImage && sync
```

> **为什么要用 U-Boot 加载而不是直接换文件**：换文件需要先能启动。这是鸡生蛋问题。

### 2.2 用 initramfs 进内存系统，修文件系统

如果是 ext4 脏页 / 日志损坏导致卡住（典型症状：内核启动到某个时间点停住不动，
或者反复报 `EXT4-fs error`），需要进一个**不依赖 eMMC 的临时系统**：

```sh
# PC 上开 TFTP 服务，把内核和 initramfs 放进去
SOC# tftpboot 0x9000000003000000 uImage-99pi
SOC# tftpboot 0x900000000a000000 initrd_la.uimg
SOC# setenv bootargs console=ttyS0,115200 rdinit=/sbin/init
SOC# bootm 0x9000000003000000 0x900000000a000000
```

进了内存系统后：

```sh
e2fsck -y /dev/mmcblk0p1
# 修完断电重启，正常启动流程会自动挂载
```

> ⚠ ramdisk 必须打上 **legacy uImage 头且 arch 字节为 27（IH_ARCH_LA）**，
> 否则 U-Boot 会报 `Ramdisk image is corrupt`。用 `_工具/patch-uimage-arch.py` 改。

### 2.3 新版内核先 TFTP 试跑，别落盘

**改内核的正确流程**，绕过了一切砖化风险：

```sh
# 1. PC 上开 TFTP，把新内核放进目录
SOC# tftpboot 0x9000000003000000 uImage-new
SOC# setenv bootargs console=ttyS0,115200 root=/dev/mmcblk0p1 rootfstype=ext4 rw rootwait
SOC# bootm 0x9000000003000000
# 2. 跑通、WiFi 能连、SSH 能进 → 才写进 eMMC
```

```sh
# 3. 在已启动的系统里落盘（先备份！）
cp /boot/uImage /boot/uImage.bak-$(date +%Y%m%d)
cp /tmp/uImage-new /boot/uImage && sync
```

### 2.4 U-Boot 本身砖了

**这块板的结论：不要自己编 U-Boot。**

实测自编译版本会 `TLB exception!`，而厂商提供的 `u-boot-spl-gz.bin`（580,432 字节）正常。
恢复需要 EJTAG 或 USB 烧录，代价很高——所以**没有正当理由就别动 SPI NOR 里的引导程序**。

---

## 三、这块板的 U-Boot 特殊之处

### 3.1 加载地址必须写成两个 cell

```sh
# 对
ext4load mmc 0:1 0x9000000003000000 /boot/uImage

# 错（会被截断）
ext4load mmc 0:1 0x9000000003000000 ...
```

LoongArch64 的地址是 64 位，U-Boot 的 `load` 命令对它要求 `<高32位> <低32位>` 两段格式。
这个板的 FIT 加载地址 `0x9000000000200000`、入口 `0x9000000001329000`。

### 3.2 autoboot 是 0 秒

U-Boot 编译时 `CONFIG_BOOTDELAY=0`，上电直接引导，没有等待窗口。

**唯一办法是持续发送 `c`**（用 `_工具/uart-bridge/serial-cmd.ps1` 后台循环发），
人工按复位根本对不上。这也是为什么要珍惜“能进系统”的每一次机会。

### 3.3 没有 `gpio` 命令

U-Boot 里没有编译 GPIO 子命令，想在引导阶段操作引脚得用 `mw` 直接写寄存器，
基本不现实。所以**所有引脚操作都必须在 Linux 里做**。

### 3.4 FIT 需要 CONFIG_FIT 和 CONFIG_LZMA

自编 U-Boot 如果缺这两个配置，会分别报：

```
Wrong Image Format          ← 缺 CONFIG_FIT
Unimplemented compression type 3   ← 缺 CONFIG_LZMA（lzma 压缩的 FIT）
```

---

## 四、踩坑教训

按代价从大到小排列。

| 教训 | 说明 |
|---|---|
| **别直接覆盖 `/boot/uImage`** | 一定要先 TFTP + `bootm` 验证能启动，确认没问题再落盘。直接覆盖是本次唯一一次把板子搞到起不来的原因 |
| **改内核前先备份** | 当时做了 `uImage.bak-6.9rc7`，所以还能救回来。没备份就只能 EJTAG |
| **不要用设备树 `gpio-hog` 拉高引脚** | 实测 GPIO81 用 gpio-hog 会导致 `GPIOS 0..15 (GPA0) failed to register, -16`，进而**全系统 deferred probe**（eMMC、网卡全挂）。必须用运行时脚本操作 `/sys/class/gpio` |
| **厂商源码树可能不自洽** | `linux-6.12` 树里 `dwmac-loongson.c`、`pinctrl-loongson2.c` 的 API 与内核核心版本不匹配，编不过。换树不如换版本 |
| **`CONFIG_DTB_MATCH_BY_BOARD_NAME` 没有兜底** | 开了这个配置后必须在命令行传 `board_name=xxx`，否则选不到 DTB 且**不会回退到内置 DTB**，表现为静默早死 |
| **Go 程序基本都能跑，Rust/C 要小心** | OpenList / frp / Xray 官方 loong64 二进制直接可用；Rust 官方预编译产物因含 LSX 指令在本核上 SIGILL |
| **U 盘枚举失败后拔插没用** | dwc2 控制器卡在异常状态（`can't set config #1, error -110`）时，必须**整机断电 5 秒** |
| **自编 U-Boot 会 TLB exception** | 用厂商的 `u-boot-spl-gz.bin`，别自己编 |

---

## 五、预防措施（三条铁律）

1. **内核永远先 TFTP 试跑**，确认能启动、网络正常、SSH 能进，再写 eMMC
2. **每次改 `/boot/uImage` 前先 `cp` 一份带日期的备份**
3. **SPI NOR 里的 U-Boot 不要动**——除非你准备好了 EJTAG

---

## 六、相关文档

- [05-切换到新世界.md](05-切换到新世界.md) —— 正常情况下的内核切换流程
- [10-Alpine系统安装记录.md](10-Alpine系统安装记录.md) —— 根文件系统怎么装的
- [13-无头部署清单.md](13-无头部署清单.md) —— 跑起来之后的运维手册
