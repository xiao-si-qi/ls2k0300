# -*- coding: utf-8 -*-
"""Read-only TFTP server with RFC 2348 blksize negotiation.

Why blksize matters: classic TFTP sends 512-byte blocks and waits for an ACK
per block, so throughput is limited by round-trip time, not by link bandwidth.
U-Boot is single-threaded here, so 512-byte blocks cap us around 3.7 MiB/s on a
gigabit link. Negotiating 1468-byte blocks (largest that fits a 1500-byte MTU)
gives roughly 3x.

PMON/U-Boot compatibility note: DATA must come from UDP port 69, not an
ephemeral port, or the client drops it (WinError 10054 on Windows).
"""
import os
import socket
import struct
import sys

sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = r"C:\Users\xiaosiqi\Documents\esp32\2k0300\03-BSP\out"
HOST = "0.0.0.0"
PORT = 69
DEFAULT_BLKSIZE = 512
MAX_BLKSIZE = 1468          # keep under a 1500-byte Ethernet MTU

OP_RRQ, OP_WRQ, OP_DATA, OP_ACK, OP_ERROR, OP_OACK = 1, 2, 3, 4, 5, 6


def log(m):
    print(m, flush=True)


def parse_request(data):
    parts = data[2:].split(b"\x00")
    if len(parts) < 2:
        return None, None, {}
    fname = parts[0].decode("utf-8", "replace")
    mode = parts[1].decode("ascii", "replace")
    opts = {}
    rest = parts[2:]
    for i in range(0, len(rest) - 1, 2):
        if rest[i]:
            opts[rest[i].lower()] = rest[i + 1]
    return fname, mode, opts


def serve(sock, data, addr):
    fname, mode, opts = parse_request(data)
    if not fname:
        return
    base = os.path.basename(fname)
    path = os.path.join(ROOT, base)

    blksize = DEFAULT_BLKSIZE
    if b"blksize" in opts:
        try:
            blksize = max(8, min(MAX_BLKSIZE, int(opts[b"blksize"])))
        except ValueError:
            blksize = DEFAULT_BLKSIZE

    log("[req] %s:%d -> %s  mode=%s blksize=%d opts=%s"
        % (addr[0], addr[1], base, mode, blksize,
           ",".join(k.decode("ascii", "replace") for k in opts) or "-"))

    if not os.path.isfile(path):
        sock.sendto(struct.pack("!HH", OP_ERROR, 1) + b"File not found\x00", addr)
        return

    size = os.path.getsize(path)

    # ---- option handshake (OACK) ----
    if opts:
        oack = struct.pack("!H", OP_OACK)
        if b"blksize" in opts:
            oack += b"blksize\x00%d\x00" % blksize
        if b"tsize" in opts:
            oack += b"tsize\x00%d\x00" % size
        if b"timeout" in opts:
            oack += b"timeout\x00" + opts[b"timeout"] + b"\x00"
        sock.sendto(oack, addr)
        ok = False
        for _ in range(8):
            try:
                ack, peer = sock.recvfrom(2048)
            except (socket.timeout, ConnectionResetError):
                continue
            if peer[0] == addr[0] and len(ack) >= 4:
                op, num = struct.unpack("!HH", ack[:4])
                if op == OP_ACK and num == 0:
                    ok = True
                    break
        if not ok:
            log("[err] no ACK for OACK, falling back to 512")
            blksize = DEFAULT_BLKSIZE

    # ---- data transfer ----
    blk, sent = 1, 0
    next_report = 8
    with open(path, "rb") as f:
        while True:
            chunk = f.read(blksize)
            pkt = struct.pack("!HH", OP_DATA, blk) + chunk
            ok = False
            for _ in range(10):
                sock.sendto(pkt, addr)
                try:
                    ack, peer = sock.recvfrom(2048)
                except (socket.timeout, ConnectionResetError):
                    continue
                if peer[0] != addr[0] or len(ack) < 4:
                    continue
                op, num = struct.unpack("!HH", ack[:4])
                if op == OP_ERROR:
                    log("[err] client TFTP error: %s" % ack[4:].decode("utf-8", "replace"))
                    return
                if op == OP_ACK and num == blk:
                    ok = True
                    break
            if not ok:
                log("[err] block %d not acked, abort" % blk)
                return

            sent += len(chunk)
            pct = 100.0 * sent / size
            if pct >= next_report:
                log("[....] %d / %d bytes (%.1f%%)" % (sent, size, pct))
                next_report += 8
            if len(chunk) < blksize:
                break
            blk = (blk + 1) & 0xFFFF

    log("[done] %s: %d / %d bytes" % (base, sent, size))
    log("[done] COMPLETE" if sent >= size else "[warn] INCOMPLETE")


def main():
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind((HOST, PORT))
    sock.settimeout(300.0)
    log("TFTP on udp/%d root=%s max blksize=%d" % (PORT, ROOT, MAX_BLKSIZE))
    while True:
        try:
            data, addr = sock.recvfrom(2048)
        except (socket.timeout, ConnectionResetError):
            continue
        if len(data) >= 2 and struct.unpack("!H", data[:2])[0] == OP_RRQ:
            serve(sock, data, addr)


if __name__ == "__main__":
    main()
