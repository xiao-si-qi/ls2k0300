# 长时间录制 COM10，用于捕捉板子复位后的启动输出
param(
    [string]$Port = 'COM10',
    [int]$Seconds = 60,
    [string]$OutFile = 'C:\Users\xiaosiqi\Documents\esp32\2k0300\_工具\uart-bridge\capture.log'
)

$sp = New-Object System.IO.Ports.SerialPort($Port, 115200, [System.IO.Ports.Parity]::None, 8, [System.IO.Ports.StopBits]::One)
$sp.ReadTimeout = 200
$sp.Open()
$sp.DtrEnable = $false
$sp.RtsEnable = $false
Start-Sleep -Milliseconds 300
$sp.DiscardInBuffer()

$sb = New-Object System.Text.StringBuilder
$sw = [System.Diagnostics.Stopwatch]::StartNew()
while ($sw.Elapsed.TotalSeconds -lt $Seconds) {
    try { $c = $sp.ReadExisting() } catch { $c = $null }
    if ($c) { [void]$sb.Append($c) }
    Start-Sleep -Milliseconds 40
}
$sp.Close()
$sp.Dispose()

$text = $sb.ToString()
[System.IO.File]::WriteAllText($OutFile, $text, (New-Object System.Text.UTF8Encoding($false)))
Write-Host ("captured " + $text.Length + " bytes in " + $Seconds + "s")
