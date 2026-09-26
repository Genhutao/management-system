#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
学管会宿管端 · 多人同时使用压测脚本
====================================

模拟 N 名宿管"同时打开 App 干活"的完整场景，每个虚拟宿管的操作流：

    三要素登录 → 今日待办 → 上报时段 → 拍照上报 → 历史上报 → 上报详情

统计每个步骤的成功率 / P50 / P95 / 最大延迟 / 错误分布，用于定位瓶颈在哪一步。

用法：
    pip install requests
    python stress_dorm.py http://103.236.77.86:19198                  # 64 人 × 3 轮
    python stress_dorm.py http://... --users 32 --rounds 1            # 32 人快速冒烟
    python stress_dorm.py http://... --size 3000                      # 3MB 原图（未压缩）复现拥堵
    python stress_dorm.py http://... --no-upload                      # 只压读接口，不写库
    python stress_dorm.py http://... --ramp 10                        # 10 秒内陆续进入（对比同时涌入）

⚠ 三要素登录会在后端自动建号（dorm_<phone>），压测后到后台清理宿管花名册；
  上报会真实入库并可能触发 AI（若已配置），压测完建议清库或用演练库。
"""

import argparse
import io
import random
import statistics
import string
import sys
import threading
import time
from collections import Counter, defaultdict
from urllib.parse import urlparse

import requests

LOCK = threading.Lock()
LATENCY: dict[str, list[float]] = defaultdict(list)   # 步骤 -> 延迟列表
ERRORS: Counter = Counter()
DONE = Counter()                                       # 步骤 -> 总调用次数


def rand_str(n: int = 6) -> str:
    return "".join(random.choices(string.ascii_lowercase, k=n))


def fake_jpeg(size_kb: int) -> bytes:
    head = b"\xff\xd8\xff\xe0" + b"\x00" * 8
    tail = b"\xff\xd9"
    return head + random.randbytes(size_kb * 1024 - len(head) - len(tail)) + tail


class Metrics:
    """线程安全的指标收集"""

    @staticmethod
    def record(step: str, dt: float, ok: bool, err: str = ""):
        with LOCK:
            DONE[step] += 1
            if ok:
                LATENCY[step].append(dt)
            else:
                ERRORS[f"{step}:{err}"] += 1


def step(s: requests.Session, args, name: str, fn) -> None:
    t0 = time.perf_counter()
    try:
        resp = fn(s)
        dt = time.perf_counter() - t0
        Metrics.record(name, dt, resp.status_code == 200,
                       "" if resp.status_code == 200 else f"HTTP{resp.status_code}")
        return resp
    except requests.RequestException as e:
        Metrics.record(name, time.perf_counter() - t0, False, e.__class__.__name__)
        return None


def one_dorm(args, idx: int, payload: bytes, start_gate: threading.Barrier):
    """单个虚拟宿管：登录一次，然后跑 N 轮完整操作流"""
    s = requests.Session()
    phone = f"199{idx:08d}"
    name = f"压测{idx:02d}"
    building = args.building

    # 同时涌入：全员在 barrier 前待命；ramp 模式则按序号错开
    try:
        if args.ramp > 0:
            time.sleep(args.ramp * idx / args.users)
        else:
            start_gate.wait(timeout=30)
    except threading.BrokenBarrierError:
        pass

    # ---- 登录 ----
    step(s, args, "1.login", lambda s: s.post(
        f"{args.api}/auth/dorm-quick-login",
        json={"phone": phone, "building": building, "real_name": name},
        timeout=args.timeout))
    if "Authorization" not in s.headers:
        # 登录失败重试一次
        try:
            r = s.post(f"{args.api}/auth/dorm-quick-login",
                       json={"phone": phone, "building": building, "real_name": name},
                       timeout=args.timeout)
            if r.status_code == 200:
                token = r.json().get("token")
                if token:
                    s.headers["Authorization"] = f"Bearer {token}"
        except requests.RequestException:
            pass
    if "Authorization" not in s.headers:
        with LOCK:
            ERRORS["1.login:failed_twice"] += 1
            DONE["user_aborted"] += 1
        return

    for rd in range(args.rounds):
        # ---- 首页：今日待办 ----
        step(s, args, "2.today-tasks", lambda s: s.get(
            f"{args.api}/dorm/today-tasks", timeout=args.timeout))

        # ---- 时段提示 ----
        step(s, args, "3.slot-notice", lambda s: s.get(
            f"{args.api}/dorm/slot-notice", timeout=args.timeout))

        # ---- 拍照上报（第一轮或每隔 N 轮；--no-upload 跳过）----
        if not args.no_upload and rd % max(args.upload_every, 1) == 0:
            room = f"{100 + idx % 40}"
            step(s, args, "4.upload", lambda s: s.post(
                f"{args.api}/dorm/upload-photo",
                data={
                    "room_number": room,
                    "photo_type": "violation",
                    "building": building,
                    "report_kind": "photo",
                    "note_text": f"并发压测 {phone} 第{rd + 1}轮",
                },
                files={"image": (f"d{idx}_{rd}.jpg", io.BytesIO(payload), "image/jpeg")},
                timeout=args.timeout * 2))

        # ---- 历史上报 ----
        r = step(s, args, "5.inspections", lambda s: s.get(
            f"{args.api}/dorm/inspections", timeout=args.timeout))

        # ---- 上报详情（取列表第一条）----
        if r is not None and r.status_code == 200:
            try:
                items = r.json().get("items") or []
                if items:
                    iid = items[0]["id"]
                    step(s, args, "6.detail", lambda s: s.get(
                        f"{args.api}/dorm/inspections/{iid}", timeout=args.timeout))
            except (ValueError, KeyError):
                pass

        # 模拟真人操作间隔
        time.sleep(max(0.0, args.think + random.uniform(-0.5, 0.5)))


def pct(sorted_list, q):
    return sorted_list[min(len(sorted_list) - 1, int(len(sorted_list) * q))]


def summarize(args, wall: float):
    order = ["1.login", "2.today-tasks", "3.slot-notice", "4.upload", "5.inspections", "6.detail"]
    print(f"\n{'=' * 66}")
    print(f"压测汇总：{args.users} 宿管 × {args.rounds} 轮"
          f"{' × 每轮1次上报' if not args.no_upload else '（只读）'}，"
          f"单图≈{args.size}KB，墙钟 {wall:.1f}s")
    print(f"{'步骤':<16}{'调用':>6}{'成功率':>8}{'P50':>8}{'P95':>8}{'max':>8}")
    for name in order:
        lat = sorted(LATENCY.get(name, []))
        total = DONE.get(name, 0)
        if total == 0:
            continue
        okp = f"{len(lat) * 100 // total}%"
        p50 = f"{pct(lat, .5):.2f}s" if lat else "-"
        p95 = f"{pct(lat, .95):.2f}s" if lat else "-"
        mx = f"{max(lat):.2f}s" if lat else "-"
        print(f"{name:<16}{total:>6}{okp:>8}{p50:>8}{p95:>8}{mx:>8}")
    if ERRORS:
        print("失败分布（Top 10）：")
        for k, v in ERRORS.most_common(10):
            print(f"    {k}: {v}")
    print("结论参考：P95 < 2s 良好；2-5s 拥挤；>5s 或成功率 <95% 需要扩容/优化")


def seed_roster(args) -> bool:
    """用 tech_admin 把 N 个虚拟宿管写入宿管花名册，三要素登录才放行"""
    s = requests.Session()
    try:
        r = s.post(f"{args.api}/auth/login",
                   json={"username": args.seed_user, "password": args.seed_pass},
                   timeout=args.timeout)
        if r.status_code != 200:
            print(f"预置花名册失败：tech_admin 登录 HTTP {r.status_code}（--no-seed 可跳过预置）")
            return False
        s.headers["Authorization"] = f"Bearer {r.json()['token']}"
        ok = 0
        for i in range(args.users):
            rr = s.post(f"{args.api}/tech/roster-presets", json={
                "real_name": f"压测{i:02d}",
                "phone": f"199{i:08d}",
                "building": args.building,
                "floor": "全楼",
            }, timeout=args.timeout)
            if rr.status_code == 200:
                ok += 1
        print(f"花名册预置：{ok}/{args.users} 成功")
        return ok == args.users
    except requests.RequestException as e:
        print(f"预置花名册网络异常：{e}")
        return False


def main():
    p = argparse.ArgumentParser(description="宿管端多人同时使用压测")
    p.add_argument("base", nargs="?", default="http://127.0.0.1:8080")
    p.add_argument("--users", type=int, default=64, help="并发宿管数（默认 64）")
    p.add_argument("--rounds", type=int, default=3, help="每人完整操作轮数（默认 3）")
    p.add_argument("--think", type=float, default=2.0, help="步骤间思考秒数（默认 2，0=无间隔）")
    p.add_argument("--size", type=int, default=300, help="单张图 KB（默认 300=压缩后；3000=原图复现拥堵）")
    p.add_argument("--no-upload", action="store_true", help="跳过上报，只压读接口")
    p.add_argument("--upload-every", type=int, default=1, help="每 N 轮上报一次（默认每轮）")
    p.add_argument("--building", default="压测楼", help="三要素楼栋名")
    p.add_argument("--seed", action="store_true", default=True,
                   help="压测前自动用 tech_admin 把虚拟宿管写入宿管花名册（默认开）")
    p.add_argument("--no-seed", dest="seed", action="store_false", help="不预置花名册（名册里已有压测账号时用）")
    p.add_argument("--seed-user", default="tech_admin")
    p.add_argument("--seed-pass", default="123456")
    p.add_argument("--ramp", type=float, default=0, help="错峰进入秒数（默认 0=同时涌入）")
    p.add_argument("--timeout", type=int, default=30, help="单请求超时秒数")
    args = p.parse_args()
    args.api = args.base.rstrip("/") + "/api/v1"

    # 提前拦截 requests 无法接受的地址（下划线/非 ASCII 主机名 → InvalidURL）
    host = urlparse(args.base).hostname or ""
    if "_" in host or not host.isascii():
        sys.exit(
            f"地址不合法：主机名「{host}」含下划线或非 ASCII 字符，requests 会直接拒绝。\n"
            f"请使用 IP 或合法域名，例如：python stress_dorm.py http://103.236.77.86:19198"
        )

    payload = fake_jpeg(args.size)
    print(f"目标 {args.base} ｜ {args.users} 宿管 × {args.rounds} 轮 ｜ "
          f"{'含上报' if not args.no_upload else '只读'} ｜ 单图≈{args.size}KB ｜ "
          f"{'错峰 %gs' % args.ramp if args.ramp > 0 else '同时涌入'}")

    if args.seed and not args.no_seed:
        if not seed_roster(args):
            sys.exit("花名册预置失败，退出（检查 tech_admin 账号或加 --no-seed 跳过）")

    barrier = threading.Barrier(args.users)
    threads = [threading.Thread(target=one_dorm, args=(args, i, payload, barrier), daemon=True)
               for i in range(args.users)]
    start = time.perf_counter()
    for t in threads:
        t.start()
    for t in threads:
        t.join(timeout=args.timeout * (args.rounds * 8 + 10) + 60)
    wall = time.perf_counter() - start

    summarize(args, wall)


if __name__ == "__main__":
    main()
