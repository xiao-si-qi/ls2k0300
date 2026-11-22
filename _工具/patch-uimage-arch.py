# -*- coding: utf-8 -*-
"""Patch a legacy uImage header so its architecture byte says LoongArch.

U-Boot rejected the ramdisk with "No Linux LoongArch Ramdisk Image" because
mkimage has no 'loongarch' arch name, so it was tagged RISC-V. The arch byte in
the legacy header is at offset 29; LoongArch is IH_ARCH_LA = 27 here.

The header CRC (ih_hcrc, offset 4) must be recomputed afterwards.
"""
import struct
import sys
import zlib

SRC = r"C:\Users\xiaosiqi\Documents\esp32\2k0300\03-BSP\out\initrd.uimg"
DST = r"C:\Users\xiaosiqi\Documents\esp32\2k0300\03-BSP\out\initrd_la.uimg"

IH_ARCH_LA = 27

with open(SRC, "rb") as f:
    data = bytearray(f.read())

magic, hcrc, tm, size, load, ep, dcrc, os_, arch, typ, comp = struct.unpack(
    ">IIIIIIIBBBB", data[:32])

print("magic      = 0x%08x (expect 0x27051956)" % magic)
print("os         = %d (IH_OS_LINUX=5)" % os_)
print("arch       = %d -> will become %d" % (arch, IH_ARCH_LA))
print("type       = %d (IH_TYPE_RAMDISK=3)" % typ)
print("comp       = %d (IH_COMP_GZIP=1)" % comp)
print("data size  = %d" % size)

if magic != 0x27051956:
    sys.exit("not a legacy uImage")

data[29] = IH_ARCH_LA
# zero the header crc field, then recompute over the 64-byte header
data[4:8] = b"\x00\x00\x00\x00"
new_hcrc = zlib.crc32(bytes(data[:64])) & 0xFFFFFFFF
data[4:8] = struct.pack(">I", new_hcrc)

with open(DST, "wb") as f:
    f.write(data)

print("patched -> %s" % DST)
print("new hcrc   = 0x%08x" % new_hcrc)
print("file size  = %d" % len(data))
