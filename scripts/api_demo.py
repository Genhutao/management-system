#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
学管会综合管理系统 · API 调用演示脚本
================================

演示后端主要接口的调用方式（对照《API接口文档.md》）：

  1. 通用登录            POST /api/v1/auth/login            （部长/部员/技术组等）
  2. 宿管三要素免密登录   POST /api/v1/auth/dorm-quick-login  （手机号+楼栋+姓名）
  3. 个人信息            GET  /api/v1/auth/profile
  4. 宿管今日待办        GET  /api/v1/dorm/today-tasks
  5. 宿管上报时段        GET  /api/v1/dorm/slot-notice
  6. 宿管历史上报        GET  /api/v1/dorm/inspections
  7. 拍照上报（三态）    POST /api/v1/dorm/upload-photo      （multipart）
  8. 违纪台账分页检索    GET  /api/v1/deductions
  9. 台账 CSV 导出       GET  /api/v1/deductions/export-csv
 10. 部员积分流水        GET  /api/v1/member/score-history
 11. 我的班次            GET  /api/v1/member/my-shifts
 12. 极速请假            POST /api/v1/member/leave
 13. 部长请假审批列表    GET  /api/v1/minister/leaves
 14. 招新信息（公开）    GET  /api/v1/recruit/info
 15. 打表（需技术副部长）POST /api/v1/deductions             （可能 403，演示权限门禁）

用法：
    pip install requests
    python api_demo.py                       # 默认 http://127.0.0.1:8080
    python api_demo.py http://192.168.1.50:8080

⚠ 对接注意（详见文档 §9）：
  - HTTP 200 ≠ 成功：多数写接口丢弃数据库错误仍返回 200，需检查业务字段
  - 空列表序列化为 null 而非 []
  - ai_status 当前恒为 disabled/failed，扣分建议字段会被清零，需按"人工核对"设计
"""

import io
import json
import sys
import requests

BASE = sys.argv[1].rstrip("/") if len(sys.argv) > 1 else "http://103.236.77.86:19198"
API = BASE + "/api/v1"
TIMEOUT = 15

# 演示账号（backend/internal/repository/db.go 种子数据，密码均为 123456）
DEMO = {
    "dorm_manager": "dorm_ay_liu",
    "member": "member_li",
    "minister": "minister_zhang",
    "tech_admin": "tech_admin",
    "viewer_export": "export_admin",
}
PASSWORD = "123456"


def show(title: str, resp: requests.Response, body: bool = True, limit: int = 600):
    """统一打印：状态码 + 响应体片段"""
    print(f"\n{'=' * 60}\n[{title}]  {resp.request.method} {resp.request.url}")
    print(f"HTTP {resp.status_code}")
    if not body:
        return
    try:
        text = json.dumps(resp.json(), ensure_ascii=False, indent=2)
    except ValueError:
        text = resp.text
    print(text[:limit] + ("  …(截断)" if len(text) > limit else ""))


def err(resp: requests.Response) -> str:
    """取后端统一的 {"error": "..."} 字段"""
    try:
        return resp.json().get("error", "")
    except ValueError:
        return resp.text[:120]


# ---------------------------------------------------------------- 1. 通用登录
def login(username: str, password: str = PASSWORD) -> str:
    print(f"\n>>> 以 {username} 登录 …")
    r = requests.post(f"{API}/auth/login", json={"username": username, "password": password}, timeout=TIMEOUT)
    show("通用登录", r)
    r.raise_for_status()
    return r.json()["token"]


def auth(token: str) -> dict:
    return {"Authorization": f"Bearer {token}"}


# ------------------------------------------------- 2. 宿管三要素免密登录
def dorm_quick_login(phone: str, building: str, real_name: str) -> str:
    """
    ⚠ 楼栋是双向子串匹配：请传完整楼栋名（如 "12号楼"），
      传 "1" 会误匹配任意包含 1 的楼栋。
    成功后自动建号 username=dorm_<phone>，初始密码 123456。
    """
    print(f"\n>>> 宿管三要素登录：{phone} / {building} / {real_name}")
    r = requests.post(
        f"{API}/auth/dorm-quick-login",
        json={"phone": phone, "building": building, "real_name": real_name},
        timeout=TIMEOUT,
    )
    show("宿管三要素登录", r)
    r.raise_for_status()
    return r.json()["token"]


# ------------------------------------------------------- 4-7. 宿管工作台
def dorm_flow(token: str):
    h = auth(token)

    r = requests.get(f"{API}/dorm/today-tasks", headers=h, timeout=TIMEOUT)
    show("今日待办（is_in_work_time 硬编码 12-13 / 18-22 点）", r)

    r = requests.get(f"{API}/dorm/slot-notice", headers=h, timeout=TIMEOUT)
    show("上报时段", r)

    r = requests.get(f"{API}/dorm/inspections", headers=h, timeout=TIMEOUT)
    show("历史上报（硬编码 Limit 50）", r, limit=400)

    # 6. 拍照上报 —— 实拍形态（report_kind != "text" 时无图直接 400）
    print("\n>>> 演示缺图被拒（后端契约：无图 400）…")
    r = requests.post(
        f"{API}/dorm/upload-photo",
        headers=h,
        data={"room_number": "302", "photo_type": "violation", "report_kind": "photo"},
        timeout=TIMEOUT,
    )
    show("无图上报应被拒", r)

    # 用本地生成的 1x1 PNG 演示 multipart 上传
    png = bytes.fromhex(
        "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489"
        "0000000d4944415478da63fcffff3f030005fe02fea72d1e480000000049454e44ae426082"
    )
    r = requests.post(
        f"{API}/dorm/upload-photo",
        headers=h,
        data={
            "room_number": "302",
            "photo_type": "violation",
            "building": "1号楼",
            "report_kind": "photo",
            "note_text": "演示：发现违规电器",
            "subject_names": "张三\n李四",  # 换行/、/，均可分隔
        },
        files={"image": ("demo.png", io.BytesIO(png), "image/png")},
        timeout=TIMEOUT,
    )
    show("拍照上报（multipart）", r, limit=800)
    print("⚠ ai_status 不会是 real：图片为相对路径，外部模型取不到；扣分建议按人工核对处理")


# ------------------------------------------------------- 8-9. 台账与导出
def deduction_flow(token: str):
    h = auth(token)

    r = requests.get(
        f"{API}/deductions",
        headers=h,
        params={"page": 1, "page_size": 5, "status": "confirmed"},
        timeout=TIMEOUT,
    )
    show("违纪台账（分页，仅未撤销）", r, limit=800)
    data = r.json()
    # ⚠ total_deduct_sum 已排除 revoked，但 items 里仍包含 revoked 记录
    print(f"合计扣分(排除撤销)：{data.get('total_deduct_sum')}，本页 {len(data.get('items') or [])} 条")

    r = requests.get(f"{API}/deductions/export-csv", headers=h, timeout=TIMEOUT)
    r.encoding = "utf-8-sig"  # 带 BOM
    show("台账 CSV 导出", r, body=False)
    lines = r.text.splitlines()
    print("\n".join(lines[:4]) + ("\n  …" if len(lines) > 4 else ""))


# --------------------------------------------------- 10-13. 部员与部长
def member_flow(token: str):
    h = auth(token)
    r = requests.get(f"{API}/member/score-history", headers=h, timeout=TIMEOUT)
    show("部员积分流水", r, limit=500)

    r = requests.get(f"{API}/member/my-shifts", headers=h, timeout=TIMEOUT)
    show("我的班次（按姓名匹配，同名会互见）", r, limit=400)

    # 极速请假：shift_id 需要真实存在的班次；这里演示参数校验
    r = requests.post(f"{API}/member/leave", headers=h, json={"shift_id": 0, "reason": "演示请假"}, timeout=TIMEOUT)
    show("请假（无效 shift_id 的返回）", r)


def minister_flow(token: str):
    h = auth(token)
    r = requests.get(f"{API}/minister/leaves", headers=h, params={"status": "pending"}, timeout=TIMEOUT)
    show("待审批请假列表", r, limit=400)

    r = requests.get(f"{API}/minister/week-duty-status", headers=h, timeout=TIMEOUT)
    show("本周出勤三色大盘", r, limit=400)


# --------------------------------------------------- 14-15. 公开与门禁
def public_and_gate_flow(member_token: str):
    r = requests.get(f"{API}/recruit/info", timeout=TIMEOUT)
    show("招新信息（免登录公开）", r)

    # 部员直接打表 → 403（requireDeductionAuthority 门禁演示）
    h = auth(member_token)
    r = requests.post(
        f"{API}/deductions",
        headers=h,
        json={
            "building": "1号楼", "floor": "3F", "room_number": "302",
            "student_name": "张三", "class_name": "高一(1)班",
            "category": "违规电器", "deduct_points": 5, "reason": "演示：门禁测试",
        },
        timeout=TIMEOUT,
    )
    show("普通部员打表（预期 403：仅技术部副部长/技术组可打表）", r)
    if r.status_code == 403:
        print(f"   后端说明：{err(r)}")


def main():
    print(f"目标后端：{BASE}\n（请确认服务已启动且地址可达；演示账号来自种子数据）")

    # 公开接口
    public_and_gate_flow("")

    # 部长 / 部员
    minister_token = login(DEMO["minister"])
    minister_flow(minister_token)
    member_token = login(DEMO["member"])
    member_flow(member_token)

    # 台账（member/minister 可读）
    deduction_flow(minister_token)

    # 宿管端：三要素登录 → 工作台 → 上报
    # ⚠ 需要技术组先在后台 /tech/roster-presets 预置该宿管（手机号+楼栋+姓名）
    try:
        dorm_token = dorm_quick_login("13800000001", "1号楼", "刘阿姨")
        dorm_flow(dorm_token)
    except (requests.HTTPError, KeyError) as e:
        print(f"\n宿管流程跳过：{e}\n（三要素需先在后台花名册预置，或改用 dorm_manager 账号登录）")
        dorm_flow(login(DEMO["dorm_manager"]))

    print("\n全部演示完成。完整接口清单见 API接口文档.md（85 个接口）")


if __name__ == "__main__":
    main()
