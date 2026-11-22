# Flash the diag firmware, then reset the C3 and capture its boot output.
# ASCII-only: PowerShell 5.1 parses non-BOM scripts as ANSI.
$ErrorActionPreference = 'Continue'
$port = 'COM10'
$build = 'C:\Users\xiaosiqi\AppData\Local\Temp\uart-diag'

Write-Host "==== step 1: flash ===="
Push-Location "$build\build"
& python -m esptool --chip esp32c3 -p $port -b 460800 --before default_reset --after hard_reset `
    write_flash 0x0 bootloader\bootloader.bin 0x8000 partition_table\partition-table.bin 0x10000 uart_diag.bin 2>&1 |
    Select-Object -Last 6
Pop-Location

Start-Sleep -Milliseconds 400

Write-Host "`n==== step 2: open port, pulse reset, capture ===="
try {
    $sp = New-Object System.IO.Ports.SerialPort($port, 115200, [System.IO.Ports.Parity]::None, 8, [System.IO.Ports.StopBits]::One)
    $sp.ReadTimeout = 200
    $sp.WriteTimeout = 500
    $sp.Open()

    # reset pulse: RTS -> EN, DTR -> GPIO9 (keep high so it boots normally)
    $sp.DtrEnable = $false
    $sp.RtsEnable = $true
    Start-Sleep -Milliseconds 120
    $sp.RtsEnable = $false
    Start-Sleep -Milliseconds 100
    $sp.DiscardInBuffer()

    $sb = New-Object System.Text.StringBuilder
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    while ($sw.ElapsedMilliseconds -lt 3000) {
        try { $c = $sp.ReadExisting() } catch { $c = $null }
        if ($c) { [void]$sb.Append($c) }
        Start-Sleep -Milliseconds 50
    }

    Write-Host "---- boot output ($($sb.Length) bytes) ----"
    Write-Host ($sb.ToString() -replace "`r", "")

    Write-Host "`n==== step 3: echo test ===="
    $sp.DiscardInBuffer()
    $token = "HELLO-12345"
    $sp.Write("$token`r`n")
    Start-Sleep -Milliseconds 800
    $echo = $sp.ReadExisting()
    if ($echo -and $echo.Contains('HELLO')) {
        Write-Host "ECHO OK -> PC side (UART0/CH343) works both directions" -ForegroundColor Green
        Write-Host ($echo -replace "`r", "")
    }
    else {
        Write-Host "ECHO FAIL -> got $($echo.Length) bytes" -ForegroundColor Red
        if ($echo) { Write-Host ($echo -replace "`r", "") }
    }

    $sp.Close()
    $sp.Dispose()
}
catch {
    Write-Host "serial error: $($_.Exception.Message)" -ForegroundColor Red
}
