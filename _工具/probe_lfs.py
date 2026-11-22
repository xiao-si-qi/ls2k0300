# -*- coding: utf-8 -*-
"""Try to fetch LFS-tracked PDFs without the LFS API."""
import json, ssl, sys, urllib.parse, urllib.request, urllib.error

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

CTX = ssl.create_default_context()
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/124.0 Safari/537.36"}


def head(url, maxread=8):
    req = urllib.request.Request(url, headers=UA)
    try:
        with urllib.request.urlopen(req, timeout=30, context=CTX) as r:
            data = r.read(maxread)
            return r.status, r.headers.get("Content-Type"), r.headers.get("Content-Length"), data
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Content-Type"), None, b""
    except Exception as e:
        return "ERR", f"{type(e).__name__}: {e}", None, b""


OWNER, REPO, BR = "lasocer", "Loongson-2K0300-99PAI", "master"
FILES = [
    "久久派官方手册/LS2K0300久久派_V1.1_板卡使用手册_V1.2_20240705.pdf",
    "久久派 WIFI 版电路原理图/2K0300_99PAI_V1.1_0531.pdf",
    "久久派官方手册/2k300_99派FAQ.pdf",
]

print("========== A. gitee raw 接口 ==========")
for f in FILES:
    url = f"https://gitee.com/{OWNER}/{REPO}/raw/{BR}/" + urllib.parse.quote(f)
    st, ct, cl, d = head(url)
    print(f"[{st}] {f[:46]:46s} ct={ct} len={cl} head={d[:8]!r}")

print("\n========== B. gitcode 镜像 ==========")
for f in FILES[:1]:
    url = "https://gitcode.com/ctci/Loongson-2K0300-99PAI/raw/master/" + urllib.parse.quote(f)
    st, ct, cl, d = head(url)
    print(f"[{st}] ct={ct} len={cl} head={d[:8]!r}")

print("\n========== C. GitHub loongson-community/docs ==========")
for path in ["repos/loongson-community/docs/contents/",
             "repos/loongson-community/docs/contents/loongson-2k0300",
             "repos/loongson-community/docs/contents/2k0300"]:
    url = "https://api.github.com/" + path
    try:
        req = urllib.request.Request(url, headers={**UA, "Accept": "application/vnd.github+json"})
        with urllib.request.urlopen(req, timeout=25, context=CTX) as r:
            items = json.loads(r.read())
        print(f"-- {path}: {len(items)} items")
        for it in items[:40]:
            print(f"     [{it['type']:4s}] {it['name']}")
    except urllib.error.HTTPError as e:
        print(f"-- {path}: HTTP {e.code}")
    except Exception as e:
        print(f"-- {path}: {type(e).__name__}: {e}")
