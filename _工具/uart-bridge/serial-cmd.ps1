# 鍚?COM10 鍙戦€佸懡浠ゅ苟鎹曡幏鍥炴樉
# 鐢ㄦ硶: powershell -File serial-cmd.ps1 -Cmd "ifaddr syn0 192.168.55.50","ping 192.168.55.1"
param(
    [string]$Port = 'COM10',
    [int]$Baud = 115200,
    [string]$Cmd = '',
    [int]$WaitAfter = 1500,
    [int]$SettleMs = 400
)
$ErrorActionPreference = 'Continue'

$sp = New-Object System.IO.Ports.SerialPort($Port, $Baud, [System.IO.Ports.Parity]::None, 8, [System.IO.Ports.StopBits]::One)
$sp.ReadTimeout = 200
$sp.WriteTimeout = 800

try {
    $sp.Open()
    $sp.DtrEnable = $false
    $sp.RtsEnable = $false
    Start-Sleep -Milliseconds $SettleMs
    $sp.DiscardInBuffer()

    $sb = New-Object System.Text.StringBuilder
    foreach ($c in ($Cmd -split ";;")) {
        Write-Host (">>> " + $c) -ForegroundColor Cyan
        $sp.Write($c + "`r")

        # 鏀堕泦杩欎竴鏉″懡浠ょ殑鍥炴樉
        $sw = [System.Diagnostics.Stopwatch]::StartNew()
        while ($sw.ElapsedMilliseconds -lt $WaitAfter) {
            try { $chunk = $sp.ReadExisting() } catch { $chunk = $null }
            if ($chunk) { [void]$sb.Append($chunk) }
            Start-Sleep -Milliseconds 60
        }
    }

    $out = $sb.ToString()
    Write-Host ""
    Write-Host "==== 涓插彛鍥炴樉 ====" -ForegroundColor Yellow
    Write-Host ([regex]::Replace($out, "`r", ""))
}
catch {
    Write-Host ("ERR: " + $_.Exception.Message) -ForegroundColor Red
}
finally {
    if ($sp.IsOpen) { $sp.Close() }
    $sp.Dispose()
}
