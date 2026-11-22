# -*- coding: utf-8 -*-
"""Inventory the 99PAI repo: which files landed as real content vs LFS pointers."""
import os, subprocess, sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

REPO = r"C:\Users\xiaosiqi\Documents\esp32\2k0300\06-久久派官方资料"
LFS_HEAD = b"version https://git-lfs"

out = subprocess.run(
    ["git", "-C", REPO, "ls-tree", "-r", "-z", "--name-only", "HEAD"],
    capture_output=True,
).stdout
paths = [p.decode("utf-8", "replace") for p in out.split(b"\x00") if p]

real, pointer, missing = [], [], []
for rel in paths:
    full = os.path.join(REPO, rel)
    if not os.path.isfile(full):
        missing.append(rel)
        continue
    size = os.path.getsize(full)
    with open(full, "rb") as fh:
        head = fh.read(120)
    if head.startswith(LFS_HEAD):
        pointer.append((rel, size))
    else:
        real.append((rel, size))

real.sort(key=lambda x: -x[1])

print(f"总文件 {len(paths)}  |  真实内容 {len(real)}  |  LFS 指针 {len(pointer)}  |  未检出 {len(missing)}")

print("\n===== 有真实内容的文件 =====")
for rel, size in real:
    print(f"{size/1024/1024:8.2f} MiB  {rel}")

print("\n===== 只有 LFS 指针的（拿不到）=====")
for rel, size in pointer:
    print(f"{size:6d} B     {rel}")
