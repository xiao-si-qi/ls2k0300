# -*- coding: utf-8 -*-
"""Resilient downloader for the 2k0300 resource pack.

Usage: python dl.py <manifest.txt>
Manifest lines:  <dest-relative-path> <TAB> <url>
Skips files that already exist with a non-trivial size.
"""
import os, ssl, sys, time, urllib.request, urllib.error

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

CTX = ssl.create_default_context()
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                    "(KHTML, like Gecko) Chrome/124.0 Safari/537.36"}
ROOT = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(ROOT)          # -> 2k0300/


def fetch(dest_rel, url, retries=3):
    dest = os.path.join(ROOT, dest_rel)
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    if os.path.exists(dest) and os.path.getsize(dest) > 4096:
        print(f"[skip] {dest_rel} ({os.path.getsize(dest):,} B already there)")
        return True
    for attempt in range(1, retries + 1):
        try:
            req = urllib.request.Request(url, headers=UA)
            t0 = time.time()
            with urllib.request.urlopen(req, timeout=60, context=CTX) as r, open(dest, "wb") as f:
                total = 0
                while True:
                    chunk = r.read(65536)
                    if not chunk:
                        break
                    f.write(chunk)
                    total += len(chunk)
            dt = max(time.time() - t0, 0.001)
            print(f"[ok  ] {dest_rel}  {total:,} B  {total/1024/1024:.2f} MiB  "
                  f"{total/1024/dt:.0f} KiB/s")
            return True
        except Exception as e:
            print(f"[retry {attempt}/{retries}] {dest_rel}: {type(e).__name__}: {e}")
            if os.path.exists(dest):
                os.remove(dest)
            time.sleep(2 * attempt)
    print(f"[FAIL] {dest_rel}")
    return False


def main():
    manifest = sys.argv[1]
    ok = bad = 0
    with open(manifest, encoding="utf-8-sig") as fh:
        for line in fh:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            dest_rel, url = line.split("\t", 1)
            if fetch(dest_rel.strip(), url.strip()):
                ok += 1
            else:
                bad += 1
    print(f"\n== done: {ok} ok, {bad} failed ==")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
