# -*- coding: utf-8 -*-
"""Inspect the downloaded 99PAI zip: real files vs LFS pointers."""
import os, sys, zipfile

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

ZIP = r"C:\Users\xiaosiqi\Downloads\Loongson-2K0300-99PAI-master.zip"

print(f"zip 大小: {os.path.getsize(ZIP)/1024/1024:.2f} MiB\n")

LFS_HEAD = b"version https://git-lfs"
real, pointer, other = [], [], []

with zipfile.ZipFile(ZIP) as z:
    infos = [i for i in z.infolist() if not i.is_dir()]
    print(f"条目数: {len(infos)}\n")
    for i in infos:
        # 只读前 120 字节判断是不是 LFS 指针
        with z.open(i) as fh:
            head = fh.read(120)
        if i.file_size < 200 and head.startswith(LFS_HEAD):
            pointer.append(i)
        elif i.filename.lower().endswith((".pdf", ".zip", ".tar", ".gz", ".xz", ".rar",
                                          ".bin", ".jpg", ".png", ".docx", ".md")):
            real.append(i)
        else:
            other.append(i)

print(f"真实内容文件: {len(real)}   LFS 指针: {len(pointer)}   其它: {len(other)}")

print("\n===== 真实内容（按大小）=====")
for i in sorted(real, key=lambda x: -x.file_size):
    print(f"{i.file_size/1024/1024:8.2f} MiB  {i.filename}")

print("\n===== LFS 指针（拿不到）=====")
for i in pointer:
    print(f"{i.file_size:6d} B     {i.filename}")
