#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
学管会系统 · 违纪记录轮询演示脚本
================================

只做三件事：
  1. 登录拿 token（支持普通账号 或 宿管三要素免密）
  2. 循环拉取违纪台账   GET /api/v1/deductions
  3. 下载台账里的上报图片 /uploads/...（历史记录接口返回相对路径）

用法：
    pip install requests
    python api_poll.py                              # 默认 127.0.0.1:8080，10 秒一轮
    python api_poll.py http://192.168.1.50:8080 5   # 指定地址 + 5 秒一轮

账号二选一（默认用部长演示账号）：
    python api_poll.py http://127.0.0.1:8080 10 --username minister_zhang --password 123456
    python api_poll.py ... --phone 13800000001 --building 1号楼 --name 刘阿姨   # 宿管三要素
"""

import argparse
import io
import json
import os
import re
import sys
import time

import requests

# 上报历史里图片 URL 的匹配（ InspectionPhoto.image_url 通常是 /uploads/xxx.jpg ）
UPLOAD_RE = re.compile(r"/uploads/[\w.\-]+\.(?:jpg|jpeg|png|gif|webp)", re.I)


def build_session(args) -> tuple[str, requests.Session]:
    """登录并返回 (token, session)；session 自带 Authorization 头"""
    s = requests.Session()
    if args.phone:
        print(f">>> 宿管三要素登录：{args.phone} / {args.building} / {args.name}")
        r = s.post(
            f"{args.api}/auth/dorm-quick-login",
            json={"phone": args.phone, "building": args.building, "real_name": args.name},
            timeout=args.timeout,
        )
    else:
        print(f">>> 账号登录：{args.username}")
        r = s.post(
            f"{args.api}/auth/login",
            json={"username": args.username, "password": args.password},
            timeout=args.timeout,
        )

    if r.status_code != 200:
        sys.exit(f"登录失败 HTTP {r.status_code}: {r.text[:200]}")

    token = r.json().get("token")
    if not token:
        sys.exit(f"响应里没有 token：{r.text[:200]}")
    s.headers["Authorization"] = f"Bearer {token}"
    print("登录成功，token 已缓存（有效期 7 天）\n")
    return token, s


def fetch_records(s: requests.Session, args) -> list[dict]:
    """拉一页违纪台账"""
    r = s.get(
        f"{args.api}/deductions",
        params={"page": 1, "page_size": args.page_size, "status": args.status},
        timeout=args.timeout,
    )
    if r.status_code != 200:
        print(f"拉取失败 HTTP {r.status_code}: {r.text[:150]}")
        return []
    data = r.json()
    items = data.get("items") or []  # ⚠ GORM 空集合是 null
    print(f"[{time.strftime('%H:%M:%S')}] 台账合计 {data.get('total')} 条"
          f"（合计扣分 {data.get('total_deduct_sum')}，排除已撤销），本页 {len(items)} 条")
    for it in items[:5]:
        print(f"  - {it.get('created_at', '')[:16]} {it.get('building')}-{it.get('room_number')}"
              f" {it.get('student_name')} {it.get('category')} -{it.get('deduct_points')}分"
              f" [{it.get('status')}]")
    if len(items) > 5:
        print(f"  … 等共 {len(items)} 条")
    return items


def download_images(s: requests.Session, args, text: str):
    """从响应文本里抠 /uploads/ 图片路径并下载到 save_dir"""
    urls = sorted(set(UPLOAD_RE.findall(text)))
    new = 0
    for u in urls:
        filename = os.path.join(args.save_dir, u.rsplit("/", 1)[-1])
        if os.path.exists(filename):
            continue
        try:
            img = s.get(args.base + u, timeout=args.timeout)
            if img.status_code == 200 and img.content[:4] not in (b"",):
                with open(filename, "wb") as f:
                    f.write(img.content)
                new += 1
        except requests.RequestException as e:
            print(f"  图片下载失败 {u}: {e}")
    if new:
        print(f"  新下载 {new} 张图片 → {args.save_dir}/")


def main():
    p = argparse.ArgumentParser(description="学管会违纪记录轮询演示")
    p.add_argument("base", nargs="?", default="http://127.0.0.1:8080", help="后端地址")
    p.add_argument("--interval", type=int, default=10, help="轮询间隔秒数（默认 10）")
    p.add_argument("--page-size", type=int, default=20, help="每页条数（默认 20，上限 200）")
    p.add_argument("--status", default="confirmed", choices=["confirmed", "revoked"],
                   help="只看有效记录或已撤销记录")
    p.add_argument("--username", default="minister_zhang")
    p.add_argument("--password", default="123456")
    p.add_argument("--phone", help="宿管三要素登录：手机号（提供后忽略 username/password）")
    p.add_argument("--building", default="1号楼", help="宿管楼栋（请传完整名称）")
    p.add_argument("--name", dest="name", help="宿管姓名")
    p.add_argument("--save-dir", default="downloads", help="图片保存目录")
    p.add_argument("--timeout", type=int, default=10, help="单请求超时秒数")
    args = p.parse_args()

    args.api = args.base.rstrip("/") + "/api/v1"
    args.save_dir = os.path.abspath(args.save_dir)
    os.makedirs(args.save_dir, exist_ok=True)

    try:
        _, s = build_session(args)
    except requests.RequestException as e:
        sys.exit(f"连不上后端 {args.base}：{e.__class__.__name__}\n"
                 f"请先启动服务（仓库根目录 ./start.sh，需 Linux/WSL）或确认地址。")

    # Ctrl+C 干净退出
    try:
        while True:
            try:
                items = fetch_records(s, args)
                # 台账结构里没有图片字段，顺带拉一次上报历史拿图片
                r = s.get(f"{args.api}/dorm/inspections", timeout=args.timeout)
                if r.status_code == 200:
                    download_images(s, args, r.text)
                elif r.status_code == 403:
                    pass  # 非宿管/部长角色无此接口，属正常
                else:
                    print(f"上报历史 HTTP {r.status_code}")
            except requests.RequestException as e:
                print(f"网络异常：{e}（{args.interval}s 后重试）")
            time.sleep(args.interval)
    except KeyboardInterrupt:
        print("\n已停止")


if __name__ == "__main__":
    main()
