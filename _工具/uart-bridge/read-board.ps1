# Read COM10 and check whether the LS2K0300 outputs anything.
# ASCII only on purpose: PowerShell 5.1 reads non-BOM scripts as ANSI.
$ErrorActionPreference = 'Continue'
$port = 'COM10'
$baud = 115200

try {
    $sp = New-Object System.IO.Ports.SerialPort($port, $baud, [System.IO.Ports.Parity]::None, 8, [System.IO.Ports.StopBits]::One)
    $sp.ReadTimeout = 300
    $sp.WriteTimeout = 500
    $sp.Open()

    # CH343 DTR/RTS usually wire to ESP32 GPIO9 / EN. Keep them deasserted.
    $sp.DtrEnable = $false
    $sp.RtsEnable = $false
    Start-Sleep -Milliseconds 300
    $sp.DiscardInBuffer()

    $sb = New-Object System.Text.StringBuilder
    $sw = [System.Diagnostics.Stopwatch]::StartNew()

    # Phase 1: listen 3 s
    while ($sw.ElapsedMilliseconds -lt 3000) {
        try { $c = $sp.ReadExisting() } catch { $c = $null }
        if ($c) { [void]$sb.Append($c) }
        Start-Sleep -Milliseconds 60
    }

    # Phase 2: send 'c' a few times to try to break into PMON
    for ($i = 0; $i -lt 8; $i++) {
        $sp.Write('c')
        Start-Sleep -Milliseconds 120
        try { $c = $sp.ReadExisting() } catch { $c = $null }
        if ($c) { [void]$sb.Append($c) }
    }
    $sp.Write("`r")

    # Phase 3: listen 4 more seconds
    $sw.Restart()
    while ($sw.ElapsedMilliseconds -lt 4000) {
        try { $c = $sp.ReadExisting() } catch { $c = $null }
        if ($c) { [void]$sb.Append($c) }
        Start-Sleep -Milliseconds 60
    }

    $sp.Close()
    $sp.Dispose()

    $text = $sb.ToString()
    Write-Host "==== received $($text.Length) bytes ===="
    if ($text.Length -eq 0) {
        Write-Host "(no output at all)" -ForegroundColor Yellow
    }
    else {
        Write-Host "---- raw text ----"
        Write-Host ($text -replace "`r", "")
        Write-Host ""
        Write-Host "---- first 64 bytes hex ----"
        $bytes = [System.Text.Encoding]::GetEncoding(28591).GetBytes($text)
        $n = [Math]::Min(64, $bytes.Length)
        (($bytes[0..($n-1)] | ForEach-Object { $_.ToString('x2') }) -join ' ')
    }
}
catch {
    Write-Host "open failed: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host "ports: $([System.IO.Ports.SerialPort]::GetPortNames() -join ', ')"
}
