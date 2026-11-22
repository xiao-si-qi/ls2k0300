# 桥接回环自测
#
# 用途：在把 ESP32 接到久久派之前，先确认桥本身是通的。
# 做法：用一根杜邦线短接 ESP32 的对板 TX / RX 两个引脚，然后跑这个脚本。
#
# ESP32-C3 : 短接 GPIO6 和 GPIO7
# ESP32 经典款 : 短接 GPIO17 和 GPIO16
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File loopback-test.ps1 -Port COM10
#   powershell -ExecutionPolicy Bypass -File loopback-test.ps1 -Port COM10 -Baud 115200

param(
    [string]$Port = 'COM10',
    [int]$Baud = 115200,
    [int]$WaitMs = 1500
)

$ErrorActionPreference = 'Stop'

function Write-Info($msg, $color = 'Gray') { Write-Host $msg -ForegroundColor $color }

Write-Info "== 回环自测 ==" "Cyan"
Write-Info "端口 : $Port @ $Baud 8N1"
Write-Info "前提 : 已用杜邦线短接对板 TX / RX"
Write-Info ""

if (-not ([System.IO.Ports.SerialPort]::GetPortNames() -contains $Port)) {
    Write-Info "找不到 $Port。当前可用端口: $([System.IO.Ports.SerialPort]::GetPortNames() -join ', ')" 'Red'
    exit 1
}

$token = 'LOOPBACK-' + (Get-Random -Minimum 100000 -Maximum 999999)
$sp = New-Object System.IO.Ports.SerialPort($Port, $Baud, [System.IO.Ports.Parity]::None, 8, [System.IO.Ports.StopBits]::One)
$sp.ReadTimeout = 300
$sp.WriteTimeout = 1000

try {
    $sp.Open()
    Start-Sleep -Milliseconds 200
    $sp.DiscardInBuffer()

    Write-Info "发送: $token"
    $sp.Write("$token`r`n")

    $sb = New-Object System.Text.StringBuilder
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    while ($sw.ElapsedMilliseconds -lt $WaitMs) {
        try {
            $chunk = $sp.ReadExisting()
            if ($chunk) { [void]$sb.Append($chunk) }
        } catch { }
        Start-Sleep -Milliseconds 50
    }
    $got = $sb.ToString()
}
finally {
    if ($sp.IsOpen) { $sp.Close() }
    $sp.Dispose()
}

Write-Info ""
if ($got.Contains($token)) {
    Write-Info "PASS —— 桥是通的，可以接久久派了" 'Green'
    Write-Info "       切记：接板子之前把那根短接线拆掉！" 'Yellow'
    exit 0
}

Write-Info "FAIL —— 收到 $($got.Length) 字节" 'Red'
if ($got.Length -gt 0) {
    Write-Info "收到内容:"
    Write-Info ($got -replace "`r", '')
    Write-Info ""
    Write-Info "收到东西但不是自己发的 -> 可能 GPIO 接错了，或者你还没短接" 'Yellow'
} else {
    Write-Info "完全没收到数据，逐项排查：" 'Yellow'
}

Write-Info @'

  1. 短接线是否真的接在「对板 TX」和「对板 RX」上？
       ESP32-C3  : GPIO6 <-> GPIO7
       ESP32 经典: GPIO17 <-> GPIO16
  2. 引脚号是否和你烧的固件一致（看 main.c 里的 DEV_TX_PIN / DEV_RX_PIN）
  3. 串口是否选对（设备管理器里 "USB-Enhanced-SERIAL CH343" 那个口）
  4. 板子是否有电（LED 亮不亮）
  5. 是否烧的是桥接固件（烧完重新上电，串口里应该只有 ROM 启动信息，没有 IDF 日志）
'@
exit 2
