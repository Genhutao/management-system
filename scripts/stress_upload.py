#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
上报并发压测脚本（模拟 N 名宿管同时拍照上报）
============================================

复现/验证"32 人并发上报网络拥堵"问题：
  - 每个虚拟宿管先用三要素登录（自动建号），再并发提交 upload-photo
  - 图片默认 300KB（模拟 App 端压缩后的大小）；--size 可调成 3000 复现原图 3MB 的拥堵
  - 统计：成功率、P50/P95 延迟、失败原因分布

用法：
    pip install requests
    python stress_upload.py http://103.236.77.86:19198              # 32 人，300KB 图
    python stress_upload.py http://103.236.77.86:19198 --size 3000  # 3MB 原图复现拥堵
    python stress_upload.py http://... --users 8 --rounds 3         # 8 人跑 3 轮

⚠ 三要素登录会在后端自动建号（dorm_<phone>），压测后建议在后台清理花名册。
"""

import argparse
import io
import random
import statistics
import string
import sys
import threading
import time

import requests

Lock = threading.Lock()
RESULTS: list[dict] = []


def rand_str(n: int = 6) -> str:
    return "".join(random.choices(string.ascii_lowercase, k=n))


def fake_jpeg(size_kb: int) -> bytes:
    """生成近似 JPEG 大小的随机字节（不需要真图片，只看传输）"""
    head = b"\xff\xd8\xff\xe0" + b"\x00" * 8
    tail = b"\xff\xd9"
    return head + random.randbytes(size_kb * 1024 - len(head) - len(tail)) + tail


def one_user(args, idx: int, payload: bytes):
    s = requests.Session()
    phone = f"199{idx:08d}"
    name = f"压测{idx:02d}"
    building = args.building
    rec = {"user": phone, "login": None, "uploads": [], "errors": []}

    t0 = time.perf_counter()
    try:
        r = s.post(
            f"{args.api}/auth/dorm-quick-login",
            json={"phone": phone, "building": building, "real_name": name},
            timeout=args.timeout,
        )
        rec["login"] = time.perf_counter() - t0
        if r.status_code != 200:
            rec["errors"].append(f"login:{r.status_code}")
            with Lock: RESULTS.append(rec); return
        token = r.json()["token"]
        s.headers["Authorization"] = f"Bearer {token}"
    except requests.RequestException as e:
        rec["login"] = time.perf_counter() - t0
        rec["errors"].append(f"login:{e.__class__.__name__}")
        with Lock: RESULTS.append(rec); return

    for rd in range(args.rounds):
        t = time.perf_counter()
        try:
            r = s.post(
                f"{args.api}/dorm/upload-photo",
                data={
                    "room_number": f"{100 + idx}",
                    "photo_type": "violation",
                    "building": building,
                    "report_kind": "photo",
                    "note_text": f"压测 {phone} 第{rd + 1}轮",
                },
                files={"image": (f"s{idx}_{rd}.jpg", io.BytesIO(payload), "image/jpeg")},
                timeout=args.timeout,
            )
            dt = time.perf_counter() - t
            rec["uploads"].append((dt, r.status_code))
            if r.status_code != 200:
                rec["errors"].append(f"upload:{r.status_code}")
        except requests.RequestException as e:
            dt = time.perf_counter() - t
            rec["uploads"].append((dt, 0))
            rec["errors"].append(f"upload:{e.__class__.__name__}")
    with Lock:
        RESULTS.append(rec)


def summarize(args):
    lat = [dt for r in RESULTS for dt, code in r["uploads"]]
    ok = [dt for r in RESULTS for dt, code in r["uploads"] if code == 200]
    total = len(lat)
    print(f"\n{'=' * 56}\n压测结果：{args.users} 人 × {args.rounds} 轮，单图 ≈{args.size}KB")
    print(f"  成功 {len(ok)}/{total}（{len(ok) * 100 // max(total, 1)}%）")
    if lat:
        lat_sorted = sorted(lat)
        p = lambda q: lat_sorted[min(len(lat_sorted) - 1, int(len(lat_sorted) * q))]
        print(f"  延迟  P50={p(.5):.1f}s  P95={p(.95):.1f}s  max={max(lat):.1f}s")
    errs = {}
    for r in RESULTS:
        for e in r["errors"]:
            errs[e] = errs.get(e, 0) + 1
    if errs:
        print("  失败分布：")
        for k, v in sorted(errs.items(), key=lambda x: -x[1]):
            print(f"    {k}: {v}")


def main():
    p = argparse.ArgumentParser(description="上报并发压测")
    p.add_argument("base", nargs="?", default="http://127.0.0.1:8080")
    p.add_argument("--users", type=int, default=32, help="并发宿管数（默认 32）")
    p.add_argument("--rounds", type=int, default=1, help="每人上报轮数（默认 1）")
    p.add_argument("--size", type=int, default=300, help="单张图片大小 KB（默认 300；3000=原图复现拥堵）")
    p.add_argument("--building", default="压测楼", help="三要素楼栋名")
    p.add_argument("--timeout", type=int, default=30, help="单请求超时秒数（默认 30）")
    args = p.parse_args()
    args.api = args.base.rstrip("/") + "/api/v1"

    payload = fake_jpeg(args.size)
    print(f"目标 {args.base} ｜ {args.users} 并发 × {args.rounds} 轮 ｜ 单图 ≈{args.size}KB")
    print(f"理论瞬时上行 ≈ {args.users * args.size / 1024:.1f} MB\n")

    start = time.perf_counter()
    threads = [threading.Thread(target=one_user, args=(args, i, payload)) for i in range(args.users)]
    for t in threads: t.start()
    for t in threads: t.join()
    print(f"全部完成，墙钟 {time.perf_counter() - start:.1f}s")
    summarize(args)


if __name__ == "__main__":
    main()
