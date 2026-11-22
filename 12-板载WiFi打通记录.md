# 久久派板载 WiFi（AIC8800）在新世界打通记录

> 2026-11-22 实测成功。板卡：LS2K300-99PI-WIFI，新世界（U-Boot + Linux 6.9.0-rc7）
> 结果：开机自动连接 `666-5G`（5GHz WiFi 6），DHCP 到 `192.168.1.253`，外网可达

---

## 一、最终状态

```
$ iw dev wlan0 link
Connected to 90:b9:42:8f:2d:c6
    SSID: 666-5G
    freq: 5200.0            ← 5GHz
    signal: -85 dBm
    rx bitrate: 17.2 MBit/s  HE-MCS 0 HE-NSS 1
    tx bitrate: 34.4 MBit/s  HE-MCS 3 HE-NSS 1

$ ip -4 addr show wlan0
inet 192.168.1.253/24

$ ping -c 3 223.5.5.5
3 packets transmitted, 3 received, 0% packet loss, min/avg/max = 21.6/23.9/26.6 ms
```

模组信息（从 SDIO 枚举得出）：
- 厂商/设备 ID：`0xc8a1` / `0x0082`(func1)、`0x0182`(func2)
- 芯片：**AIC8800D80**，chip rev 7
- 支持 SDIO 3.0，最高时钟 150 MHz（驱动自设）

---

## 二、为什么之前一直不行（完整因果链）

这块板子的 WiFi 从来没在新世界上跑通过，包括**厂商自己也没验证**（他们原话："理论上 wifi 版也可编译该内核，但须在设备树中加入缺失的 sdio 模块"）。原因有四个，缺一不可：

### 坑 1：SDIO1 控制器默认关闭

6.9 树的 `loongson_2k0300.dtsi` 里：

```dts
sdio1: sdio1@0x16148000 {
    status = "disabled";
    ...
};
```

WiFi 版 dts **没有覆盖它**。必须加：

```dts
&sdio1 {
    status = "okay";
    pinctrl-names = "default";
    pinctrl-0 = <&sdio1_6bit>;
};
```

### 坑 2：pwm2/pwm3 抢走 SDIO1 的数据线

官方《2K0300 引脚功能复用表》：

```
SDIO1_CLK  = GPIO100     SDIO1_D0 = GPIO102     SDIO1_D2 = GPIO104 (还复用 pwm[2])
SDIO1_CMD  = GPIO101     SDIO1_D1 = GPIO103     SDIO1_D3 = GPIO105 (还复用 pwm[3])
```

厂商 dts 里 `pwm2`/`pwm3` 是 `okay` 且带 `pinctrl-0`，一上电就占住 pin 104/105：

```
pin GPA96-8 already requested by 1611b020.pwm; cannot claim for 16148000.sdio1
```

**必须把 pwm2/pwm3 解绑**（运行时 `unbind` 到 `ls-pwm` 驱动即可，不用改 DTS）。

### 坑 3：WLAN_EN 没拉高（模组没上电）

官方复用表 + 原理图第 20 页：

```
TIM1_CH1 = GPIO81  ←  原理图网络表：WLAN_EN ←→ CPU_TIM1_CH1
```

模组规格书（CDW-20800D4，主芯片 AIC8800D40）：

```
pin 12  WL_DIS  I  Power key (L=OFF, H=ON)     ← 高电平开启
pin  9  VDD     P  3.3V
pin 22  VDDIO   P  1.8V 或 3.3V
pin 24  LPO     —  悬空即可（不需要外部 32k）
```

**必须把 GPIO81 拉高**，否则模组不上电，SDIO 初始化直接超时：

```
mmc1: Failed to initialize a non-removable card
```

> ⚠ 不要用 DTS 的 `gpio-hog` 实现！实测会让 GPIO 子系统注册失败：
> `GPIOS 0..15 (GPA0) failed to register, -16`，然后全系统 deferred probe。
> 用 `/sys/class/gpio` 从用户态设置才安全。

### 坑 4：固件路径写死在 `/vendor/etc/firmware`

驱动编译参数里写死：

```
-DCONFIG_AIC_FW_PATH="/vendor/etc/firmware"
```

放 `/lib/firmware/` 不认，会报：

```
aicbsp: aicbsp_set_subsys, err2      ← 其实是 aicbsp_driver_fw_init 失败
```

**固件必须放到 `/vendor/etc/firmware/`**

---

## 三、完整操作序列（就是开机脚本做的事）

```sh
M=/lib/modules/$(uname -r)

# 0) 清掉可能残留的旧模块
rmmod aic8800_fdrv 2>/dev/null
rmmod aic8800_bsp  2>/dev/null

# 1) 释放 SDIO1 的 D2/D3 引脚
for p in 1611b020.pwm 1611b030.pwm; do
    [ -e "/sys/bus/platform/drivers/ls-pwm/$p" ] && \
        echo "$p" > /sys/bus/platform/drivers/ls-pwm/unbind
done

# 2) 给模组上电（GPIO81 = TIM1_CH1 = WLAN_EN）
[ -d /sys/class/gpio/gpio81 ] || echo 81 > /sys/class/gpio/export
echo out > /sys/class/gpio/gpio81/direction
echo 1   > /sys/class/gpio/gpio81/value
sleep 1

# 3) 重绑 sdio1，让 MMC 重新探测卡
echo 16148000.sdio1 > /sys/bus/platform/drivers/ls2k_sdio/unbind
sleep 1
( echo 16148000.sdio1 > /sys/bus/platform/drivers/ls2k_sdio/bind ) &
sleep 6
# 成功标志： dmesg 出现 "mmc1: new high speed SDIO card at address xxxx"

# 4) 按依赖顺序加载模块
insmod "$M/net/rfkill/rfkill.ko"
insmod "$M/net/wireless/cfg80211.ko"
insmod "$M/drivers/net/wireless/aic8800/aic8800_bsp/aic8800_bsp.ko"
sleep 2
insmod "$M/drivers/net/wireless/aic8800/aic8800_fdrv/aic8800_fdrv.ko"
sleep 4
# 成功标志： /sys/class/net/wlan0 出现

# 5) 连接
ip link set wlan0 up
wpa_supplicant -B -i wlan0 -c /etc/wpa_supplicant/wpa_supplicant.conf -D nl80211
sleep 12
udhcpc -i wlan0 -q -n -t 10
```

---

## 四、模块依赖关系（不要多加载）

```
rfkill.ko  →  cfg80211.ko  →  aic8800_bsp.ko  →  aic8800_fdrv.ko
```

**不需要 mac80211**——aic8800 是 fullmac 风格驱动，直接对接 cfg80211。

漏 `rfkill` 会报：
```
cfg80211: Unknown symbol rfkill_register (err -2)
```

---

## 五、内核与源码

| 项 | 值 |
|---|---|
| 源码树 | `chitao1234/linux-99pi-plus`（厂商新世界维护者，树内自带 aic8800 驱动） |
| 版本 | 6.9.0-rc7 |
| 配置 | `arch/loongarch/configs/ls2k0300_99_pai_wifi_defconfig` |
| 打包 | 树内自带 `build_99pi_uimage.sh`（lzma 压缩 + 从 System.map 取 kernel_entry 生成 FIT） |
| 产物 | `uImage-99pi-nohog`（9,146,199 B），入口 `0x9000000001329000` |
| 板子上的 | `/boot/uImage`（备份：`uImage.bak-vendor`） |

> 我们对他树做的唯一改动：**移除 `wlan_en-hog`**（那个 hog 会让 GPIO 子系统崩溃），
> 改成开机脚本从用户态拉高 GPIO81。

---

## 六、开机自启服务

`/etc/init.d/wifi-bringup`（openrc，已 `rc-update add wifi-bringup default`）：

完整脚本见本节附件，核心就是上面第三节的序列，要点：
- **幂等**：先 `rmmod` 再走全流程，重复执行不会把 `wlan0` 弄丢
- `wpa_supplicant` 路径是 **`/sbin/`** 不是 `/usr/sbin/`
- 用 `wpa_supplicant -B` 自己 daemonize，不要套 `start-stop-daemon`

WiFi 配置 `/etc/wpa_supplicant/wpa_supplicant.conf`：

```
ctrl_interface=/var/run/wpa_supplicant
update_config=1

network={
	ssid="666"
	psk="<见 SECRETS.local.md>"
	key_mgmt=WPA-PSK
	priority=2
}

network={
	ssid="666-5G"
	psk="<见 SECRETS.local.md>"
	key_mgmt=WPA-PSK
	priority=1
}
```

> 实测这台板子只能扫到 `666-5G`（5GHz，-85 dBm 偏弱），2.4G 的 `666` 没扫到。

---

## 七、注意事项

1. **信号只有 -85 dBm** —— 很可能天线（IPEX）没接或距离远。接上天线速率会好很多。
2. **eth0 路由优先** —— 有线路由 metric 205 < WiFi 307，默认走有线。要测纯 WiFi 可以 `ip route del default via 192.168.137.1`。
3. **重启后 eth0 的 IP 会变**（PC 的 ICS DHCP），但 wlan0 稳定拿 `192.168.1.x`（路由器 DHCP）。
4. 这个方案依赖 **chitao 内核**，换其他内核（比如厂商原版）`wlan0` 不会工作，因为原版没有 aic8800 驱动。
5. `rc-service wifi-bringup status` 会显示 `stopped`——这是正常的，它是一次性脚本不是守护进程。

---

## 八、参考资料

- 仓库：<https://github.com/chitao1234/linux-99pi-plus>
- 发布页（含预编译模块和固件）：<https://github.com/chitao1234/linux-99pi-plus/releases>
- 官方论坛发布帖：<https://bbs.ctcisz.com/forum.php?mod=viewthread&tid=139>
- 模组规格书：`2K0300_99PAI_V1.1` 配套的 `CDW.20800D4_Series_specification_V1.3`
- 芯片引脚复用表：`04-参考/引脚复用表/2K0300引脚功能复用表.md`
- 原理图第 8 页（WIFI/BT 网络）、第 20 页（CPU TIM 引脚）
