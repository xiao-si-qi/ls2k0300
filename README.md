# 龙芯 2K0300（久久派）实战笔记

一块国产 LoongArch64 开发板从**开箱到无头部署**的完整记录：换内核、打通板载 WiFi、装系统、配存储、内网穿透、跑代理、接入 Home Assistant。

> 板卡型号：**LS2K300-99PI-WIFI**（深圳中科云 / 久久派）
> SoC：**Loongson 2K0300** —— LA264 单核 @1GHz，LoongArch64，**无 LSX/LASX 向量单元**
> 内存：432 MB 可用 · 存储：8 GB eMMC + USB

---

## 这个仓库里有什么

不是"Hello World 教程"，是**踩坑记录**。每一篇都对应一个真实卡住过的问题，以及最后为什么那样解决。

| 文档 | 内容 |
|---|---|
| [00-索引.md](00-索引.md) | 资料包总索引 |
| [05-切换到新世界.md](05-切换到新世界.md) | PMON + Linux 4.19（ABI 1.0）→ U-Boot + Linux 6.9（ABI 2.0）的完整迁移步骤 |
| [10-Alpine系统安装记录.md](10-Alpine系统安装记录.md) | 装 Alpine 3.21 到 eMMC，含 8 个坑 |
| [11-当前状态与明日待办.md](11-当前状态与明日待办.md) | 阶段性交接记录 |
| [12-板载WiFi打通记录.md](12-板载WiFi打通记录.md) | **AIC8800 SDIO WiFi 从零打通**，4 个必须同时满足的条件 |
| [13-无头部署清单.md](13-无头部署清单.md) | 拔掉串口后的运维手册：访问方式、8 个自启服务、故障排查表 |
| [14-性能测试报告.md](14-性能测试报告.md) | CPU / 加密 / 内存 / 存储 / 网络 实测数据 |
| [03-BSP/src/switchyard-agent-go/](03-BSP/src/switchyard-agent-go/) | 把板子接入 Home Assistant 的 Go 设备代理（完整实现 Switchyard 协议） |

---

## 几个值得单独说的结论

### 1. 板载 WiFi 需要四个条件同时满足

```
① 解绑 pwm2/pwm3（它们占着 SDIO1 的 D2/D3 引脚）
② GPIO81 拉高（= TIM1_CH1 = WLAN_EN，模组电源使能）
③ 重绑 sdio1 让 MMC 重新探测卡
④ 固件必须放 /vendor/etc/firmware（驱动编译时写死的路径）
```

少任何一个都起不来。完整的逆向过程见 [12-板载WiFi打通记录.md](12-板载WiFi打通记录.md)。

> ⚠ 不要用设备树的 `gpio-hog` 实现 GPIO81 拉高 —— 实测会导致 GPIO 子系统注册失败，
> 进而全系统 deferred probe（eMMC、网卡全挂）。

### 2. LA264 没有向量单元，这决定了软件选型

```
$ cat /proc/cpuinfo
Features: cpucfg lam fpu crc32      ← 没有 LSX / LASX
```

后果：

- **Rust 官方预编译的 loongarch64 程序会 SIGILL**。Rust 从 1.84 起给
  `loongarch64-unknown-linux-{gnu,musl}` 默认开启 LSX 目标特性，
  而官方 `std` 就是按带 LSX 的基线编的 —— 连 hello world 都跑不起来。
  实测崩溃点在 `alloc::io::impls::<impl Write for Vec<u8>>::write_vectored`。
  要跑 Rust 程序必须用 `-Z build-std` 从源码重建 std。
- **Go 完全没问题**。Go 的 loong64 后端不使用向量指令，
  OpenList / frp / Xray / sing-box 都能直接跑官方二进制。

**选软件的实际优先级：Go 程序 > 有官方 loong64 二进制的 C/C++ > 源码编译 > 别碰 Rust 预编译产物。**

### 3. USB 枚举失败时，拔插 U 盘没用

这块板的 dwc2 控制器一旦卡在异常状态：

```
usb 1-1: can't set config #1, error -110
```

**必须给板子整体断电 5 秒**，重新枚举才能恢复。所以 `usb-mount` 服务做成了
每 15 秒轮询的守护进程，而不是开机挂一次。

### 4. 性能定位

| 项目 | 实测 |
|---|---|
| 整数运算 | 16.3 Mops/s |
| FP64 浮点 | 81.7 Mops/s |
| AES-128-GCM | **21.0 MB/s**（无 AES 硬件加速） |
| 内存读 / 拷 / 写 | 1750 / 606 / 796 MB/s |
| eMMC 读 / 写 | 62.2 / 55.2 MB/s |
| 有线链路 | 1000 Mb/s 全双工 |

大致相当于 **ARM Cortex-A53 @1GHz 单核**（略低，因为没 NEON）。
适合跑 SSH / MQTT / 文件服务 / 轻量 Web；不适合加密隧道跑满带宽、视频转码、Docker。

---

## 最终部署形态

```
                      ┌──────────── LS2K0300 (Alpine 3.21, Linux 6.9) ────────────┐
                      │  sshd  syslog  openlist  xray                           │
  公网 ──frp──────────│  frpc  usb-mount  wifi-bringup  switchyard-agent        │
                      │  117 GB U盘 (ext4) · 代理 20888 · OpenList 5432         │
                      └─────────────────────────────────────────────────────────┘
                                    │ MQTT
                                    ▼
                            Home Assistant (Switchyard)
```

8 个 openrc 服务全部开机自启，重启验证通过。

---

## 关于凭据

仓库里所有密码、token、订阅链接**都已替换成占位符**。
真实值保存在本地 `SECRETS.local.md`（已被 `.gitignore` 排除）。

如果你要复用这套部署，需要自己准备：

- SSH 密码 / 密钥
- frps 的 `auth.token`
- MQTT 账号密码
- WiFi PSK 和代理节点配置

---

## 硬件与资料出处

- SoC 手册、原理图、BSP 来自龙芯与板卡厂商公开发布的资料
- 新世界内核来自社区分支 [chitao1234/linux-99pi-plus](https://github.com/chitao1234/linux-99pi-plus)
- Switchyard 设备管理器：[xiao-si-qi/switchyard](https://github.com/xiao-si-qi/switchyard)

## 许可

[MIT](LICENSE) —— 文档和代码统一使用 MIT 许可，可自由使用、修改、分发。
