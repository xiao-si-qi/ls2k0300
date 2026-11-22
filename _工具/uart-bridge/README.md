# ESP32 当 USB-TTL 用：给久久派接串口控制台

## 为什么需要这个

刷 U-Boot 只能走 PMON 命令行的 `fload`，而 PMON 的命令行**只在 UART0 上**。
板上 Type-C 只供电，不带串口。所以没有 USB-TTL 就敲不了命令。

而 ESP32 开发板上本来就有一颗 USB 转串口芯片（CH340/CP2102），
让 ESP32 跑一个"字节搬运工"，就能把它变成一根 3.3V TTL 串口线。

```
电脑 ──USB──> [CH340/CP2102] ──UART0──> ESP32 ──UART2(GPIO16/17)──> 久久派 UART0
```

电平正好匹配：ESP32 GPIO 是 3.3V，久久派 UART0 也是 3.3V LVTTL。

---

## ⚠ 最大的坑：不要用 CONFIG_ESP_CONSOLE_NONE

这个坑花了两小时，记录下来免得再踩。

最初为了"让桥接独占串口、不要日志干扰"，把 sdkconfig 设成：

```
CONFIG_ESP_CONSOLE_NONE=y
```

结果：**UART0 能发不能收**。PC 发出去的字节一个都进不来，
表现为 PC→C3→PC 回环永远 0 字节，但心跳（纯发送）正常。

换成标准控制台、只关日志：

```
CONFIG_ESP_CONSOLE_UART_DEFAULT=y
CONFIG_LOG_DEFAULT_LEVEL_NONE=y
CONFIG_BOOTLOADER_LOG_LEVEL_NONE=y
```

**立刻通了。**

另外在 `app_main` 里加了一次 UART0 内部回环开关做 warm-up
（`uart_set_loop_back(UART0,true)` → 写一字节 → `false` → `uart_flush_input`），
实测能消除 RX 未就绪的状态。

定位这个问题靠的是三层自检：
1. `esp_rom_printf` 心跳 → 证明程序在跑（但 CONSOLE_NONE 时它也被静音了）
2. UART1 内部回环 `uart_set_loop_back(UART1,true)` → 证明驱动和 UART1 正常，不需要外部线
3. UART0 内部回环 → 证明芯片侧 RX 路径正常，问题在配置而非硬件

---

## 本机现状（已就绪，回环已验证 PASS）

| 项 | 值 |
|---|---|
| 桥接板 | ESP32-C3 (QFN32) rev v0.3，4 MB flash |
| MAC | `60:55:f9:77:47:70` |
| USB 桥芯片 | **CH343**（VID_1A86:55D3），枚举为 **COM10** |
| 固件 | 已烧录并**通过端到端回环测试**，`prebuilt/uart_bridge_c3_merged.bin` |
| 对板引脚 | **TX = GPIO6**，**RX = GPIO7** |
| 串口参数 | 115200 8N1 |

> 端到端验证：把 GPIO6/7 短接后跑 `loopback-test.ps1`，`LOOPBACK-139613` 原样回显 → PASS。

---

## 接线

久久派用的是 **2×15 IO 排针**（板上有丝印，以丝印/原理图为准）：

| ESP32 | 方向 | 久久派排针 | 说明 |
|---|---|---|---|
| `GPIO16` (RX) | ← | **pin 4** TXD | 板子发、ESP32 收 |
| `GPIO17` (TX) | → | **pin 6** RXD | ESP32 发、板子收 |
| `GND` | — | **pin 1** GND | **必须共地** |
| — | — | 3.3V | **不要接**，两边各自供电 |

> 接反了不会烧，只是收不到数据。没波形先交换 TX/RX 试一次。

---

## 编译与烧录

### 直接用预编译好的（推荐，已用 IDF 5.5.4 编过 target=esp32）

`prebuilt/uart_bridge_merged.bin` 是合好的整片镜像，一条命令烧进去：

```bash
# 用 IDF 环境里的 esptool（COMx 换成你板子的口）
python -m esptool --chip esp32 -p COMx -b 460800 write_flash 0x0 prebuilt/uart_bridge_merged.bin
```

合片参数：`bootloader@0x1000` + `partition-table@0x8000` + `app@0x10000`，dio / 2MB / 40MHz。

| 文件 | 用途 |
|---|---|
| `prebuilt/uart_bridge_merged.bin` | 整片烧录，最省事 |
| `prebuilt/uart_bridge.bin` | 只烧 app（0x10000），板子上已有其它 IDF 固件时用 |
| `prebuilt/bootloader.bin` / `partition-table.bin` | 分开烧时需要 |

> ⚠ 这份预编译是 **target=esp32（经典款）**。如果你手上是 ESP32-S3 / C3 / C6，
> 必须自己重编：`idf.py set-target esp32s3`（并且把 `main.c` 里的
> `DEV_TX_PIN/DEV_RX_PIN` 改成你那块板子引出的 UART 引脚，S3 常用 17/18）。

### 自己编

```bash
cd 2k0300/_工具/uart-bridge
idf.py set-target esp32        # 换成你实际的芯片：esp32s3 / esp32c3
idf.py build
idf.py -p COMx flash
```

烧完**不要**跑 `idf.py monitor`（它会占用串口、还可能触发复位）。
直接用串口终端（PuTTY / WindTerm / MobaXterm）。

> 如果你用的是 ESP32-S3/C3 这类**原生 USB** 的板子，`Serial` 走的是 USB-CDC，
> 可以省掉 CH340 那一层，直接把 GPIO17/16 接到久久派即可，代码不用改。

---

## 先自测，再接板子

用一根杜邦线把**对板 TX 和 RX 短接**（C3 就是 GPIO6 ↔ GPIO7），然后跑：

```powershell
powershell -ExecutionPolicy Bypass -File loopback-test.ps1 -Port COM10
```

看到 `PASS` 说明桥是通的，拆掉短接线去接久久派。
看到 `FAIL` 它会把收到的内容和排查清单打出来。

也可以手动测：串口终端（115200 8N1）里随便打字，应该**原样回显**。

接上久久派后给板子上电，终端里应看到：

```
PMON>
```

或者完整的启动菜单。这时候**反复按小写 `c`** 就能进 PMON 命令行。

---

## 接上之后

详见 [`../../05-切换到新世界.md`](../../05-切换到新世界.md)。

刷之前先确认：

```sh
PMON> ifaddr syn0 192.168.1.178
PMON> ping 192.168.1.2
PMON> fload tftp://192.168.1.2/u-boot-with-spl-99pi-wifi.bin
```

---

## 常见问题

| 现象 | 原因 |
|---|---|
| 满屏乱码 | 波特率不是 115200，或 ESP32 板载晶振/时钟配置不对 |
| 一个字符都收不到 | TX/RX 接反，或者忘了共地 |
| 能收不能发 | 板子 RXD 那根线没接好，或 ESP32 GPIO17 被别的功能占用 |
| 终端里出现 ESP-IDF 日志 | `esp_log_level_set("*", ESP_LOG_NONE)` 没生效，检查是否烧的是这份固件 |
| 一开终端板子就复位 | 终端软件拉了 DTR/RTS，关掉流控/DTR-RTS 即可 |
| 打字有回显但板子没反应 | 你短接的回环线忘了拆 |

---

## Arduino 版（不想用 ESP-IDF 的话）

```cpp
void setup() {
  Serial.begin(115200);            // 走 CH340 那一侧
  Serial2.begin(115200, SERIAL_8N1, 16, 17);  // RX=16, TX=17
}
void loop() {
  while (Serial.available())  Serial2.write(Serial.read());
  while (Serial2.available()) Serial.write(Serial2.read());
}
```

功能一样，就是跑得糙一点（每字节一次判断），在这个场景完全够用。
