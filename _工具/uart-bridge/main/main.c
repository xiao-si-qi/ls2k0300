/*
 * ESP32 -> USB-TTL 串口桥  (v2)
 *
 * 把 ESP32 开发板当成一根 USB 转 3.3V TTL 串口线用，
 * 在没有 USB-TTL 线时给久久派（LS2K0300）接串口控制台，进 PMON 刷 U-Boot。
 *
 * 目标板自动适配：
 *   ESP32-C3（板载 CH343/CH340 接 UART0） : PC 侧 UART0，对板侧 UART1 @ GPIO6/GPIO7
 *   ESP32 经典款（板载 CH340 接 UART0）    : PC 侧 UART0，对板侧 UART2 @ GPIO17/GPIO16
 *
 * ⚠ 关键配置（踩过的坑，别改回去）：
 *   1. sdkconfig 必须是 CONFIG_ESP_CONSOLE_UART_DEFAULT=y
 *      用 CONFIG_ESP_CONSOLE_NONE 时，UART0 的 RX 路径不会正确初始化，
 *      表现为「能发不能收」——PC 发出去的字节一个都进不来。
 *      要静音日志就用 CONFIG_LOG_DEFAULT_LEVEL_NONE，
 *      不要用 CONSOLE_NONE。
 *   2. 初始化时做一次 UART0 内部回环开关（warm-up），
 *      实测能消除 RX 收不到数据的状态。
 *
 * 接线（久久派 2x15 IO 排针）：
 *   本板 TX  --> 久久派 pin6 RXD
 *   本板 RX  <-- 久久派 pin4 TXD
 *   GND      --- 久久派 pin1 GND
 *   ⚠ 不要接 3.3V，两边各自供电；但 GND 必须共地。
 */

#include <string.h>
#include <stdint.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "driver/uart.h"
#include "esp_log.h"
#include "sdkconfig.h"

#define BRIDGE_BAUD   115200
#define BUF_SZ        1024
#define UART_BUF_SZ   8192

/* PC 侧：板载 USB-UART 桥（CH343/CH340）挂在 UART0 */
#define PC_UART       UART_NUM_0

/* 对板侧 */
#if CONFIG_IDF_TARGET_ESP32C3
#  define DEV_UART      UART_NUM_1
#  define DEV_TX_PIN    6
#  define DEV_RX_PIN    7
#else
#  define DEV_UART      UART_NUM_2
#  define DEV_TX_PIN    17
#  define DEV_RX_PIN    16
#endif

static void pump_pc_to_dev(void *arg)
{
    (void)arg;
    uint8_t buf[BUF_SZ];
    for (;;) {
        int n = uart_read_bytes(PC_UART, buf, sizeof(buf), pdMS_TO_TICKS(20));
        if (n > 0) {
            uart_write_bytes(DEV_UART, (const char *)buf, n);
        }
    }
}

static void pump_dev_to_pc(void *arg)
{
    (void)arg;
    uint8_t buf[BUF_SZ];
    for (;;) {
        int n = uart_read_bytes(DEV_UART, buf, sizeof(buf), pdMS_TO_TICKS(20));
        if (n > 0) {
            uart_write_bytes(PC_UART, (const char *)buf, n);
        }
    }
}

void app_main(void)
{
    esp_log_level_set("*", ESP_LOG_NONE);

    const uart_config_t cfg = {
        .baud_rate  = BRIDGE_BAUD,
        .data_bits  = UART_DATA_8_BITS,
        .parity     = UART_PARITY_DISABLE,
        .stop_bits  = UART_STOP_BITS_1,
        .flow_ctrl  = UART_HW_FLOWCTRL_DISABLE,
        .source_clk = UART_SCLK_DEFAULT,
    };

    /* ---- PC 侧 ---- */
    ESP_ERROR_CHECK(uart_driver_install(PC_UART, UART_BUF_SZ, UART_BUF_SZ, 0, NULL, 0));
    ESP_ERROR_CHECK(uart_param_config(PC_UART, &cfg));

    /* RX warm-up：内部回环开一下再关，实测能把 RX 路径唤醒 */
    uart_set_loop_back(PC_UART, true);
    uart_write_bytes(PC_UART, "\0", 1);
    vTaskDelay(pdMS_TO_TICKS(20));
    uart_flush_input(PC_UART);
    uart_set_loop_back(PC_UART, false);
    uart_flush_input(PC_UART);

    /* ---- 对板侧 ---- */
    ESP_ERROR_CHECK(uart_driver_install(DEV_UART, UART_BUF_SZ, UART_BUF_SZ, 0, NULL, 0));
    ESP_ERROR_CHECK(uart_param_config(DEV_UART, &cfg));
    ESP_ERROR_CHECK(uart_set_pin(DEV_UART, DEV_TX_PIN, DEV_RX_PIN,
                                 UART_PIN_NO_CHANGE, UART_PIN_NO_CHANGE));

    xTaskCreate(pump_pc_to_dev, "pc2dev", 4096, NULL, 10, NULL);
    xTaskCreate(pump_dev_to_pc, "dev2pc", 4096, NULL, 10, NULL);
}
