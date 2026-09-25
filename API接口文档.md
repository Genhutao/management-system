# 学管会综合管理系统 · 后端 API 文档

**基准**：`F:\mods\学管会\backend`，Go 1.26 + Gin 1.12 + GORM + SQLite + Casbin。
**接口总数**：85（含 3 组别名路由）。本文所有字段名、状态码、角色判定均逐行核对当前源码，非按注释推断。
**核对时间**：2026-09-24（含 P0 制度修复与 B 组数据完整性改造之后的代码）。

> ⚠️ 请先读 **§8 契约变更** 和 **§9 行为告警**。P0/B 两组改动破坏了若干原有接口契约，按旧文档对接会直接失败。

---

## 1. 通用约定

| 项 | 值 |
|---|---|
| Base URL | `http://<host>:8080/api/v1`（端口取 `PORT` 环境变量，默认 8080） |
| 请求编码 | JSON（`Content-Type: application/json`）；上传为 `multipart/form-data` |
| 认证 | `Authorization: Bearer <JWT>`。HS256，有效期 **7 天**，密钥硬编码于 `pkg/jwt/jwt.go:10`，**服务端无吊销** |
| 响应编码 | 一律 UTF-8 JSON；CSV 导出带 UTF-8 BOM（`0xEF 0xBB 0xBF`） |
| 错误格式 | `{"error": "<中文说明>"}`；部分接口附带结构化补充字段（见各节） |
| 分页 | 无统一分页。`GET /deductions` → `page`/`page_size`(≤200)；`GET /tech/db/tables/:table` → `page`/`page_size`(≤100)；其余多为**硬编码 Limit**（见 §9） |
| 空集合 | GORM 切片未命中时序列化为 `null` 而非 `[]`，前端必须做空值判断 |
| CORS | `Access-Control-Allow-Origin: *` 且 `Allow-Credentials: true`（`middleware/auth.go:15`） |

**身份传递**：JWT 载荷含 `user_id / username / real_name / role / building / floor`。中间件写入 Gin context，处理器用 `c.GetUint("user_id")`、`c.Get("role")` 读取。
**重要**：打表与留痕相关接口**不使用 JWT 快照身份**，而是每次从数据库重读用户（`controller/audit.go:15`），因此调岗、停用会立即生效；其余多数接口仍信任 JWT 里的 `role`。

---

## 2. 角色与权限矩阵

### 2.1 五个角色

| 角色键 | 含义 | 认证入口 |
|---|---|---|
| `dorm_manager` | 宿管 | `POST /auth/login` 或 `POST /auth/dorm-quick-login` |
| `member` | 学管会部员 | `/auth/login` |
| `minister` | 部长 | `/auth/login` |
| `tech_admin` | 技术维护组 | `/auth/login` |
| `viewer_export` | 信息查看下载管理 | `/auth/login` |

### 2.2 Casbin 粗粒度门禁（`internal/middleware/casbin.go`）

| 角色 | 允许的 (路径, 方法) |
|---|---|
| `dorm_manager` | `/dorm/*` GET·POST；`/schedules/today` GET；`/auth/profile` GET；`/auth/security-settings` PUT；`/students/room-members` GET |
| `member` | `/member/*` `/exam/*` `/welfare/*` GET·POST；`/schedules/*` GET；`/students/room-members` GET；**`/deductions` 与 `/deductions/*` GET·POST**；`/publicity/*` GET·POST·PUT；auth 两项 |
| `minister` | `/minister/*` 全方法；`/schedules/*` `/exam/*` 全方法；`/member/*` GET·POST；`/students/*` GET·POST·DELETE；`/deductions`+`/deductions/*` GET·POST；`/dorm/inspections` GET；`/publicity/*` `/welfare/*`；auth 两项 |
| `tech_admin` | `/tech/*` 全方法 + **`/api/v1/*` 全方法**（兜底通配，非硬编码豁免） |
| `viewer_export` | `/export/*` GET·POST；`/schedules/*` GET；`/students/*` GET；`/dorm/inspections` GET；auth 两项 |

三个易踩的匹配细节：
1. `keyMatch2` 的 `/api/v1/students/*` **不匹配**裸路径 `/api/v1/students` ⇒ `GET /students` 实际只有 `tech_admin` 能访问，部长和导出人员都是 403。
2. `member` 有 `/publicity/*` 的 PUT 权限，但 `DELETE` 一律被拒（如 `DELETE /welfare/pricings/:id` 仅 `tech_admin`）。
3. Casbin 只做路径级门禁，**部门/职务属性它表达不了**，细粒度判定在处理器层（§2.3）。

### 2.3 处理器层的追加判定：`requireDeductionAuthority`

`controller/deduction_controller.go:28`。以下 6 个接口在 Casbin 之后还要过这道门，**放行条件严格为**：

```
操作者数据库账号存在（否则 401）
且 status != "disabled"（否则 403）
且满足其一：
  (a) role == "tech_admin"                      —— 不看部门职务
  (b) role ∈ {member, minister}
      AND department 包含 "技术"
      AND position == "副部长"（精确相等）
```

适用接口：`POST /deductions`、`POST /deductions/from-report`、`POST /deductions/:id/revoke`、`GET /deductions/morning-dorm-reports`、`GET /deductions/profile`、`GET /deductions/student-profiles`。

由此可推：**技术部部长（position="部长"）不能打表**；纪检部副部长也不能（部门不含"技术"）。拒绝响应为
`403 {"error":"打表权限不足：…","department":"纪检部","position":"副部长"}`。

---

## 3. 认证与账号（4 个）

### `POST /auth/login` · 公开
- 请求：`username`*、`password`*
- `200`：`{token, user: model.User}`
- 错误：`400` 参数缺失；`401` 账号不存在；`401` 密码错误；`500` 令牌签发失败
- ⚠️ **不校验 `status`**：已停用账号只要密码正确照样拿到 token（错误文案"已被禁用"不实）；账号不存在与密码错误的提示可区分 ⇒ 可用于枚举用户名。

### `POST /auth/dorm-quick-login` · 公开
- 请求：`phone`*、`building`*、`real_name`*、`floor`（**接收但完全未使用**）
- `200`：`{message, token, user}`
- 错误：`400`；`403 未在宿管预置花名册中找到匹配的手机号与姓名`；`403 楼栋信息不匹配，预置记录为：<X>`
- 副作用：命中预置花名册后**自动建号**（`username="dorm_"+phone`、`role=dorm_manager`、`department=学生宿舍宿管部`、**初始密码固定 `123456`**），并回写 `is_activated`、`bound_user_id`。
- ⚠️ 三要素（姓名/手机号/楼栋）在校园内近乎公开 ⇒ 实际等于无凭据登录；楼栋判定是**双向子串包含**，输入 `"1"` 或 `"楼"` 即可匹配任意楼栋；建号 `Create` 错误未检查，唯一键冲突时仍返回 200 且 `user` 为零值。

### `GET /auth/profile` · JWT · 全角色
- `200`：**裸 `model.User` 对象**（无 `token`/`user` 包装）；`404` 用户不存在
- 读实时库数据，可用于换票后校准本地状态。

### `PUT /auth/security-settings` · JWT · 全角色
- 请求（全部可选）：`username`、`real_name`、`phone`、`old_password`、`new_password`
- `200`：`{message, token(重签), user}`
- 错误：`404`；`400` 参数无效；`409` 用户名被占用；`400` 改密未传原密码；`401` 原密码错误；`400` 新密码 <6 位
- ⚠️ 空字符串＝不修改，**无法清空** `phone`/`real_name`；`role`/`department`/`position`/`total_score`/`status` 不可在此改；**旧 token 不会被吊销**，改密后仍有效满 7 天。

---

## 4. 宿舍纪检主干

### 4.1 宿管上报

#### `GET /dorm/today-tasks` · JWT · `dorm_manager` `tech_admin`
- 无参数；楼栋取 JWT `building`
- `200`：`{is_in_work_time, current_time, cards:[{id,title,period,building,duty_members,is_work_time,priority,action_type,prompt_text}], building}`
- ⚠️ "工作时间"是**硬编码** `12–13 点或 18–22 点`，与后台配置的时段规则无关；当日无排班时返回一张通用卡片。

#### `GET /dorm/slot-notice` · JWT · `dorm_manager` `tech_admin`
- `200`：`{server_time, active_slot, next_slot, all_rules:[model.DormTaskSlotConfig], building, is_weekend}`；slot 对象为 `{config, is_active_now, remaining_minutes, today_submitted_count, has_submitted}`
- ⚠️ 周期过滤只认字面量 `"weekdays"`/`"weekends"`，而模型与种子数据用的是 `daily/weekday/weekend/...` ⇒ **工作日/周末门控实际失效**；`next_slot` 取的是"配置顺序第一条"而非时间上最近；跨午夜窗口已正确处理。

#### `POST /dorm/upload-photo` · JWT · `multipart/form-data` · `dorm_manager` `tech_admin`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `room_number` | string | 实际必填 | 空 ⇒ `400 请填写寝室号…` |
| `photo_type` | string | 否 | 默认 `violation`；`sanitation`/`duty_supervise` 亦可；**未做枚举校验** |
| `building` | string | 否 | 缺省回落 JWT `building` |
| `report_kind` | string | 否 | `photo`/`note`/`text`；非法值按内容推断 |
| `note_text` | string | 否 | 换行归一，**按字符截断至 2000** |
| `subject_names` | string | 否 | 换行 / `、，,;；\|／/` / 空格分隔；留空则从 `note_text` 拆分 |
| `image` | file | **`report_kind != "text"` 时必填** | 落盘 `./uploads/<uuid8>_<客户端文件名>` |

- `200`：`{message, record: model.InspectionPhoto, ai_status, vision_analysis, structured_result|null, subject_total, subject_matched, subject_unmatched}`
- `structured_result` 键：`category, severity, deduct_points, summary, tags, action_advice`
- 错误：`400`（无图 / 无寝室号）；`500`（建目录、存盘、事务失败）
- ⚠️ 关键：**`ai_status` 目前不可能为 `real`**。入库图片是 `/uploads/...` 相对路径，`pkg/ai/client.go:103` 会直接拒绝（外部模型取不到），因此配了密钥 ⇒ `failed`，没配 ⇒ `disabled`；`category/deduct_points/severity` 保持零值，`status` 停在 `uploaded`。
- ⚠️ 名单被 `service.MaxSubjectsPerReport = 50` 截断（`report_helpers.go` 里的 60 上限不可达）；文件名取自客户端，**无类型、扩展名、大小校验**；上传本身不写 `operation_logs`。

#### `GET /dorm/inspections` · JWT · `dorm_manager` `minister` `viewer_export` `tech_admin`
- 查询：`category`、`severity`
- `200`：`{total, items:[model.InspectionPhoto]}`，**硬编码 `Limit(50)`**
- ⚠️ 行级隔离只对 `dorm_manager` 生效（按 `building LIKE`）；部长与导出人员可看全校所有楼栋及宿管姓名。

### 4.2 副部长核对与打表

#### `GET /deductions/morning-dorm-reports` · §2.3 严格门禁
- 查询：`scope`（非 `all` 一律按 `today`）
- `200`：`{total, items:[MorningDormReportDTO], today, scope, period_title}`
- DTO：`id, building, floor, room_number, manager_name, photo_type, report_kind, note_text, image_url, ai_status, submitted_text, category, severity, deduct_points, created_at, is_reported, is_deducted, subject_total, subjects[]`
- `subjects[]`：`id, raw_name, student_id, class_name, match_status, match_note, converted_deduction_id`
- 行为：`ai_status != "real"` 时**强制清零** `category/severity/deduct_points`，`submitted_text` 回落为 `note_text` 或固定人工核对提示；`is_deducted` 判定为该上报存在未撤销的关联扣分（或 `status='converted'`）。
- ⚠️ 一次加载最多 300 条上报 + 其全部名单 + **整张 `dorm_roster_presets`**；楼层按宿管姓名映射，同名宿管后者覆盖前者。

#### `POST /deductions` · §2.3 严格门禁
请求 `CreateDeductionRequest`：

| 字段 | 类型 | 必填 |
|---|---|---|
| `student_id` | uint | 否（给了就走名册精确校验） |
| `building` `floor` `room_number` `student_name` `class_name` `category` | string | **是** |
| `grade` | string | 否 |
| `deduct_points` | int | 是 ⇒ **0 与负数被拒**（`binding:"required"` 对 int 即"非零"） |
| `reason` | string | 是 |
| `source_inspection_id` / `source_subject_id` | uint | 否 |

- `200`：`{message, record: model.DeductionRecord, roster_linked: bool}`
- `400`：参数缺失、`来源名单条目不存在`、`名单条目与所属上报记录不匹配`、任意防冒名拒绝理由
- `409`：`{error, existing_deduction}` 同一上报条目此前已转入
- 副作用（同一事务）：写 `deduction_records`；置 `inspection_subjects.converted_deduction_id`；置 `inspection_photos.status='converted'`；写 `operation_logs`（`deduction.create`）。
- **防冒名规则**（`resolveStudent`）：全校名册为空 **或** 本寝室无任何登记 ⇒ 宽松按姓名存底并在 `message`/审计中标注；否则必须命中名册且 `room_number` 精确一致，`status='active'`；在册但在他室直接拒绝并提示"疑似寝室填报有误或冒名"。
- ⚠️ `deduct_points` 无上限；`category`/`floor` 自由文本；`source_subject_id==0` 时不校验 `source_inspection_id` 是否真实存在。

#### `POST /deductions/from-report` · §2.3 严格门禁
- 请求：`inspection_id`*、`subject_ids`*(`[]uint`)、`floor`、`category`*、`deduct_points`*、`reason`*
- `200`：`{message, count, records:[model.DeductionRecord]}`
- `400`：`上报记录不存在` / `勾选的名单条目与该上报不匹配` / `{"error":"名单中的【X】无法打表：…","subject":"X"}`
- `409`：`{error, existing_deduction}`（预检）或 `{error}`（事务内检出重复）
- 行为：**全成全败**——任一名次未通过名册核对即整体中止；楼栋与寝室号一律继承上报记录，客户端不能覆盖；`floor` 缺省按寝室号首位推算（`302 → 3F`）。审计动作 `deduction.create_batch`。

#### `POST /deductions/:id/revoke` · §2.3 严格门禁
- 请求：`{reason: string}` — **无 `binding` 标签**，但去空白后为空 ⇒ `400 撤销打表记录必须填写理由…`
- `200`：`{message, record}`（record 为内存中已置为撤销态的对象）
- `404` 记录不存在；`409` `{error, revoked_by, revoked_at}` 已撤销过
- 行为：**软撤销**——`status='revoked'` + `revoked_by/revoked_by_name/revoke_reason/revoked_at`，原始分值与时间不变；回退 `inspection_subjects.converted_deduction_id`；该上报无剩余有效扣分时把 `inspection_photos.status` 退回 `ai_analyzed`（因而可重新打表）。审计动作 `deduction.revoke`。

#### `GET /deductions` · JWT · Casbin 放行 `member` `minister` `tech_admin`
- 查询：`floor` `room` `name` `class` `building`(LIKE)、`category`(精确)、`q`(跨 6 字段模糊)、`status`(`confirmed`/`revoked`)、`page`、`page_size`(≤200)
- `200`：`{total, page, page_size, total_deduct_sum, unlinked_in_page, status_filter, items:[model.DeductionRecord]}`
- ⚠️ **本接口没有 §2.3 门禁**：任一部门的部员/部长都能读全校违纪台账。`total_deduct_sum` 排除 `revoked`，但 `items` **包含** `revoked`；`status` 传非枚举值被静默忽略（返回全量）。

#### `GET /deductions/export-csv` · JWT · `member` `minister` `tech_admin`
- 查询参数与 `GET /deductions` 相同（无分页）
- 响应：`text/csv; charset=utf-8`，`Content-Disposition: attachment; filename="学管会打表扣分明细_YYYYMMDD_HHMMSS.csv"`（**未做 RFC 5987 编码**），UTF-8 BOM
- 列：`打表流水号, 所属楼栋, 楼层, 寝室房间号, 学生班级, 违纪学生姓名, 违纪行为类别, 扣除分值(-N), 违纪具体事由详情, 打表记录人, 记录时间, 存底状态, 撤销人, 撤销时间, 撤销理由, 名册关联`
- ⚠️ 无 §2.3 门禁、无审计；文件名同秒冲突；写错误全部忽略（可能返回被截断的 200）。

### 4.3 学生德育档案（只读）

#### `GET /deductions/profile` · §2.3 严格门禁
- 查询：`student_id` 或 `name`（+`class` LIKE、`room` 精确）；两者都缺 ⇒ `400`
- `200`：**裸 `StudentDeductionProfile`** — `student_id, student_name, class_name, grade, building, room_number, bed_number, total_deduct, record_count, revoked_count, first_at, last_at, category_stats[{category,count,points}], records[], note`
- `404` 名册查无；`400` 同名多条需补条件
- ⚠️ `records`/`total_deduct` 会把历史 `student_id=0` 的同名同班记录一并计入，而 `revoked_count`/`category_stats` 只按 `student_id` 统计 ⇒ **两组数字可能不一致**；`records` 无上限。

#### `GET /deductions/student-profiles` · §2.3 严格门禁 + 最小必要
- 查询：`class`(LIKE) `grade`(精确) `building`(LIKE) `room`(精确)
- ⚠️ 非 `tech_admin` 的调用者**必须至少给一个收敛条件**，否则 `403 按最小必要原则…`
- `200`：`{total_students, with_deduction, clean_students, revoked_excluded:true, scope{...}, items:[{student_id,student_name,class_name,grade,building,room_number,total_deduct,record_count}]}`
- 仅枚举 `students.status='active'`，**硬编码 `Limit(500)`**；历史按姓名存底的记录不计入。

### 4.4 名册中心

#### `POST /students/parse-preview` · JWT · `minister` `tech_admin`
- 优先读 `multipart` 的 `file`；否则 `{"raw_text": string}`；`separator` 字段**接收但未使用**
- `200`：`{total_recognized, grade_stats{高一,高二,高三,其他}, preview_sample[], all_parsed[]}`，元素为 `ExtractedStudent`：`real_name,grade,class_name,building,room_number,bed_number,student_no,source_line`（`bed_number` 永不填充）
- ⚠️ 解析引擎会**编造默认值**：缺楼栋填 `1号楼`、缺年级填 `高一`、缺班级填 `高一(1)班`、缺姓名填 `学生`，且这些行仍算"识别成功"；读取无大小上限。

#### `POST /students/batch-import` · JWT · `minister` `tech_admin`
- 请求：`students`*(`[]model.Student`)、`overwrite`(bool)、`overwrite_confirm`（`overwrite=true` 时必须为 `REPLACE_ALL_ROSTER`）
- `200`：`{message, count}`；`400 {error, previous_count}`；`401`；`500`
- 行为：覆盖模式在同一事务内先 `DELETE FROM students`；审计 `student_roster.overwrite_import` / `student_roster.import`
- ⚠️ 客户端可传 `id`，`CreateInBatches` 会尊重它；**覆盖导入不像 `/students/clear` 那样把 `deduction_records.student_id` 归零** ⇒ 留下悬空外键；无查重、无逐行校验。

#### `GET /students` · JWT · **仅 `tech_admin`**
- 查询：`grade`(精确) `building` `room_number` `class_name`(LIKE) `keyword`(姓名/学号 LIKE)
- `200`：`{total, items:[model.Student], grade_stats[]}`
- ⚠️ `items` 硬 `Limit(100)` 但 `total` 是未截断总数；`grade_stats` 忽略所有筛选条件。

#### `GET /students/room-members` · JWT · `dorm_manager` `member` `minister` `viewer_export` `tech_admin`
- 查询：`room_number`*（精确）、`building`(LIKE)；缺寝室号 ⇒ `400`
- `200`：`{building, room_number, students:[model.Student], count}`
- ⚠️ **未按宿管所在楼栋限制**：任何登录的宿管/部员可枚举任意寝室，且返回体含 `phone`、`bed_number`。

#### `DELETE /students/clear` · JWT · `minister` `tech_admin`
- 请求：`{confirm: "DELETE_ALL_ROSTER", unlink_linked: bool}`
- `200`：`{message, cleared, unlinked}`；`400` 缺口令；`409 {error, roster_count, linked_deductions}` 存在关联记录且未 `unlink_linked`；`401`；`500`
- 行为：事务内先把 `deduction_records.student_id` 归零再删名册，**扣分记录本身完整保留**；审计 `student_roster.clear_all`。
- ⚠️ `inspection_subjects.student_id` 不会归零 ⇒ 留下悬空引用。

---

## 5. 组织、排班与积分（13 个）

| 方法与路径 | 角色 | 关键请求 | 关键响应 |
|---|---|---|---|
| `GET /member/score-history` | member minister tech | 无（取本人） | `member_name, department, total_score, duty_count, leave_count, missed_count, bonus_count, penalty_count, score_history[], dynamic_trend[]` |
| `GET /member/my-shifts` | 同上 | 无 | `{total, items:[model.ScheduleShift]}` |
| `POST /member/leave` | 同上 | `shift_id`*、`reason`*、`substitute_id`、`substitute_name`、`auto_substitute` | `{message, leave}` |
| `GET /member/leave-list` | 同上 | 无 | `{total, items:[model.LeaveRequest]}` |
| `GET /member/substitute-recommend` | 同上 | `shift_id`(query) | `{has_candidate, candidate{id,real_name,department,phone,total_score,reason}}` 或 `{has_candidate:false,message}` |
| `GET /minister/leaves` | minister tech | `status`(默认 `pending`，`all` 不过滤) | `{total, items}` |
| `POST /minister/leaves/:id/review` | minister tech | `action`*、`comment`、`auto_substitute` | `{message, leave, substitute, reason}` |
| `GET /minister/leaves/:id/substitute-preview` | minister tech | 无 | 同 recommend 结构 |
| `POST /minister/schedule-plans` | minister tech | `title`* `rule_type`* `start_date`* `end_date`*、`shift_period`、`buildings[]`、`member_pool[]`、`custom_rules` | `{message, plan, generated_count}` |
| `GET /minister/schedules` | minister tech | `date` | `{total, items}` |
| `GET /minister/members`（=`GET /tech/members` 仅 tech） | 见注 | `department`（仅 tech_admin 生效） | `department_scope, is_tech_admin, total_members, department_avg_score(字符串), top_score_member, best_duty_member, items[]` |
| `POST /minister/scores/adjust` | minister tech | `member_id`* `change_type`* `score_change`* `reason`* | `{message, total_score, log}` |
| `POST /minister/members/promote` | minister tech | `member_id`* `position`* | `{message, user}` |
| `GET /minister/week-duty-status` | minister tech | 无（按服务器当周） | `week_range, total_members, completed/unworked/missed_count, {completed,unworked,missed}_members[], all_members[], week_shifts[]` |
| `POST /minister/ai-schedule/chat` | minister tech | `messages[{role,content}]`、`image_url`、`plan_date` | `{reply, suggested_shifts[], real_members[], server_date}` |
| `POST /minister/ai-schedule/apply` | minister tech | `plan_title`、`shifts[]`(required) | `{message, plan_id, shifts_count}` |

要点（都会影响对接）：
- **`member/leave` 与 `my-shifts` 按姓名匹配班次**（`member_names LIKE`），不是按 ID ⇒ 改名即失联、互为子串的姓名会互相看到对方班次。
- **请假/审批不校验归属**：任一 `member` 可对任意 `shift_id` 提请假；任一 `minister` 可审批全校请假，且响应含 `review_comment`、替班人手机号等。
- **`action` 与 `rule_type` 都不做枚举校验**：`action="foo"` 会被原样写进 `leave.status`；`rule_type` 只有 `"weekday"` 真正生效（跳过周末），其余值一律"每天排"。
- **审批通过 + 有替班**的副作用：改写 `schedule_shifts.member_names`（子串替换，`张三` 会命中 `张三丰`）、追加 `note`、`users.total_score += 3`、写一条 `duty_substitute` 流水。**重复审批会重复加分**（非幂等）。
- **`change_type` 被丢弃**：`scores/adjust` 落库恒为 `manual_adjust`；`score_change` 为 int 且 required ⇒ **传 0 会被拒**；分值可正可负无上限，`total_score` 可变负。
- **`PromoteMember` 只改 `position` 字符串，不改 `role`**，而 Casbin 只认 `role` ⇒ 升职本身不产生任何 API 权限（打表能力由 §2.3 的部门+职务判定提供）；其部门归属检查是**双向 `Contains`**，操作者部门为空时检查直接形同不存在。
- **`ai-schedule/chat` 完全没有调用模型**：恒定返回"未来 5 天 × 4 栋楼 = 20 条"，楼栋硬编码，`image_url` 不抓取并会编造一段"OCR 结果"；无在册部员时使用占位姓名列表。`apply` 侧 `shifts: []` 能通过 `binding:"required"`（validator 对 slice 只判非 nil），随后 `req.Shifts[0]` **越界 panic ⇒ 500 纯文本**。
- `GET /minister/schedules` 硬 `Limit(100)`，`total` 即截断后的条数。
- `week_duty_status` 的"红色旷工"只有当 `schedule_shifts.status` 字面为 `missed` 时才出现，而**代码里没有任何地方写这个值**；`desc := s.Date[5:]` 在 `date` 短于 6 字符时会 panic。

---

## 6. 技术运维（10 个）与后台数据编辑器（4 个）

| 方法与路径 | 角色 | 说明 |
|---|---|---|
| `GET /tech/ai-configs` | tech | 返回 `[]model.AIConfig`，**`api_key` 明文序列化** |
| `PUT /tech/ai-configs/:id` | tech | 请求体按 `model.AIConfig` 绑定，仅复制 8 个字段；`api_key:""` = 保留原密钥；`Save` 全列覆盖且错误忽略 ⇒ 失败也报"已更新并生效" |
| `POST /tech/ai-playground/test` | tech | `{config_key}*, input_text, image_url, photo_type}` → `{status:"success"\|"failed", engine_configured, duration_ms, engine, model_name, result, error}`；**AI 失败仍返 200**；成功时 `error` 字段是字符串 `"<nil>"`；会写 `last_tested_at/last_test_result`；默认 `image_url` 是本地路径，因此视觉调试**永远不可能成功** |
| `GET/POST /tech/roster-presets` | tech | POST 从不 upsert，重复提交产生重复行（登录与楼层映射按 `real_name` 后者覆盖）；`floor` 不传会存空串并破坏三要素匹配 |
| `GET /tech/overview` | tech | `{user_count, photo_count, shift_count, paper_count, server_time, framework, ai_status}` — 后两项为**硬编码字符串**，不反映真实 AI 状态；无学生/扣分统计 |
| `GET/POST/PUT/DELETE /tech/task-slots[/:id]` | tech | POST/PUT 直接绑定 `model.DormTaskSlotConfig`，**无 binding 标签**；PUT 是显式字段拷贝 ⇒ 省略 `is_enabled` 会把规则**静默停用**；`start_time/end_time` 不校验 `HH:MM`（`Sscanf` 错误被忽略 ⇒ 静默变 0 分）；DELETE 不校验存在性、恒返 200 |

### 后台数据编辑器 `/tech/db/tables*`（仅 `tech_admin`）

- `GET /tech/db/tables` → `{total:8, items:[TableMeta]}`；`TableMeta.columns[].required` **只是给前端看的文档，服务端从不校验**。
- `GET /tech/db/tables/:table?q=&page=&page_size=` — **表白名单固定 8 个**：`students`、`dorm_roster_presets`、`users`、`schedule_shifts`、`inspection_photos`、`leave_requests`、`recruitment_applications`、`member_score_logs`；其他值 ⇒ `400 不支持的数据名单类型`。无法注入（模型硬编码），`page_size` 上限 100。
- `POST /tech/db/tables/:table` — 请求体**直接绑定对应 GORM 模型** ⇒ 全字段可写（含 `id`）。后果需知：
  - `users`：可指定 `role`/`position`/`department`/`total_score`/`username`，**密码被强制设为 bcrypt("123456")**；借此可造出一个合法的第二个技术维护组账号（并满足 §2.3）。
  - `inspection_photos`：可伪造 `category`/`deduct_points`/**`ai_status:"real"`**，而 `morning-dorm-reports` 会信任它 ⇒ 这是绕开 P0"禁止编造 AI 结论"的**唯一残留通道**。
  - `member_score_logs`：可凭空增删积分流水，但**不会同步 `users.total_score`**，两者永不校准。
- `PUT /tech/db/tables/:table/:id` — 请求体为 `map[string]interface{}`，仅剥掉 `id`/`created_at` 后整体 `Updates`；**8 个分支都丢弃 `Updates` 返回值** ⇒ 写失败仍返 200 且 `record` 是"试图写入的值"。`password_hash` 是真实列 ⇒ **可被直接改写，等于接管任意账号**。
- `DELETE /tech/db/tables/:table/:id` — 唯一保护是 `users` 的 `id==1`；删除结果不检查，不存在的 id 也报成功；硬删除，删 `students` 或 `inspection_photos` 会留下悬空外键。
- ⚠️ **以上四个写接口一律不写 `operation_logs`**，`operation_logs` 也不在白名单内（只能由服务端产生）。

---

## 7. 招新 · 测评 · 宣传 · 福利（24 个）

| 方法与路径 | 角色 | 要点 |
|---|---|---|
| `GET /recruit/info` | **公开** | 除 `total_applied` 外**全部字段硬编码**（含 `deadline: "2026-10-15 23:59:59"`） |
| `POST /recruit/apply` | **公开** | 必填 `real_name/major_and_class/target_department`；`target_department` **不校验是否为 5 个部门之一**；⚠️ `phone` 或 `building_room` 为空时会用 `real_name+class_name` 反查名册并**静默抄录该生的手机号、寝室、性别** ⇒ 匿名者可借此探测名册命中情况并污染数据 |
| `GET /public/exam/papers[/:id]`、`POST .../submit` | **公开** | 与 `/exam/*` 同一处理器 |
| `GET /exam/papers[/:id]`、`POST /exam/papers/:id/submit` | member minister tech | ⚠️ 详情与提交**都不检查 `is_published`** ⇒ 部员可遍历 id 拿到未发布试卷；`detail_results[].correct_answer` **原样回传** ⇒ 答案可被逐题采集；交卷人取自请求体而非 JWT，无次数限制；`answers` 键匹配逻辑对题号 >9 存在缺陷；多选题按字符串精确比较（`"A,B"` ≠ `"B,A"`）；匿名提交可无限写入 `exam_submissions` |
| `GET /publicity/broadcast/news` | member minister tech | `keywords`（多词按 AND 组合，比预期更严）、`category`、`date`；硬 `Limit(50)` |
| `POST /publicity/broadcast/news`、`PUT /broadcast/news/:id/toggle` | member minister tech | 号称"播音组专属"但**无任何部门校验**；请求体直接绑定模型 ⇒ 客户端可决定 `is_broadcast`（草稿直接进播报单）；`content` 不过滤 |
| `GET /publicity/broadcast/member-push` | member minister tech | 红黑榜；⚠️ 分值重算自 `SUM(member_score_logs.points)`，**无视 `users.total_score`** ⇒ 积分已兑换掉仍被表彰；`include_scores/include_reason` 两个配置开关**从未被读取**，姓名/分值/理由一律发布；`overall` 模式下黑榜无条件输出 |
| `PUT /publicity/broadcast/push-config` | member minister tech | 任意部员可改全校推送策略；目标行按 `First()` 取表中第一条而非指定 `id` |
| `GET /publicity/images`、`/publicity/gallery/random-images` | member minister tech | **纯硬编码 Unsplash 图片 id + 随机 `&sig=`**，不读写 `publicity_assets`，`count` 固定 6 不可传 |
| `GET /welfare/gateways` | member minister tech | 返回 `key_mask`（前4****后4），原始密钥不外泄；⚠️ 但 `user_remain_quota` 取的是**旧版额度表**，与实际扣减的不是同一份 |
| `POST /welfare/gateways` | member minister tech | ⚠️ 响应直接返回 `model.TechWelfareGateway`，其 `api_key` 字段**明文序列化** ⇒ 刚存的密钥被回读给调用者（任何部员）；`owner_id=0` 的行任意部员可改；`base_url` 无校验 |
| `POST /welfare/gateways/:id/probe-models` | member minister tech | 无归属校验 ⇒ 可用他人密钥发起外呼；**探测失败会编造一份模型清单并写库**，同时仍返回"🎉 成功识别到 N 个可用模型"；非 200 分支泄漏 `resp.Body` |
| `POST /welfare/gateways/:id/exchange` | member minister tech | 路径 `:id` 根本不读，网关取自请求体 `gateway_id`；三处写**无事务** ⇒ 可并发双花；错误全忽略 ⇒ 恒返"兑换成功" |
| `POST /welfare/exchange-model` | member minister tech | `is_enabled` 先于余额校验；定价查询不带部门过滤 ⇒ 可买他部（或攻击者自定价）的低价包；同样三处无事务写 |
| `POST /welfare/chat-relay` | member minister tech | ⚠️ 上游报错或非 200 时**返回一段编造的中文"AI 透传响应"（内含调用者自己的 prompt），HTTP 200，且积分/次数照扣**；`cost_per_call` 取第一条匹配 `model_key` 的定价、忽略部门；网关 owner 完全绕过额度；出站地址即用户填写的 `base_url` ⇒ **SSRF** |
| `GET /welfare/pricings` | member minister tech | 部门隔离仅在调用者 `department` 非空时生效；`department=''` 的全局行始终可见 |
| `POST /welfare/pricings` | member minister tech | 注释称"技术部部长自定义"，实际**无任何角色或部门校验**：任何部员可定价、可通过传 `department` 改他部目录、可猜 `id` 覆盖既有行 |
| `DELETE /welfare/pricings/:id` | **仅 tech** | 恒返 200，不校验存在性与归属 |

---

## 8. 契约变更（P0 制度修复 + B 组改造）

**这些是破坏性变更，旧前端与任何对接方必须同步。**

| 接口 | 变更 | 迁移方式 |
|---|---|---|
| `DELETE /deductions/:id` | **已移除** | 改用 `POST /deductions/:id/revoke`，必须带 `{"reason": "…"}`；原记录不再被物理删除 |
| `POST /deductions` | 权限收紧为 §2.3；新增 `student_id`/`source_inspection_id`/`source_subject_id`；新增 `409`（重复转入）与 `400`（防冒名） | 打表前先确认账号属"技术"部门且职务恰为"副部长"，或为技术维护组 |
| `GET /deductions/morning-dorm-reports` | 权限从"member/minister 可读"收紧为 §2.3；响应新增 `ai_status`、`report_kind`、`note_text`、`subject_total`、`subjects[]`；`ai_status != "real"` 时 `category/severity/deduct_points` 被清零、`submitted_text` 回落为人工提示 | 前端必须显示"未启用 AI/需人工核对"徽标，不能再依赖扣分建议 |
| `POST /dorm/upload-photo` | **无图片时由 200 变 400**（`report_kind != "text"`）；新增 `report_kind`/`note_text`/`subject_names`；响应新增 `ai_status`/`subject_*`；不再产生 `sample_violation.jpg` 之类占位 | 前端提交前校验有图，或显式选 `text` 形态 |
| `GET /deductions` | `total_deduct_sum` 改为**排除 revoked**；`items` 仍包含 revoked；新增 `unlinked_in_page`、`status_filter`；支持 `status=` 筛选 | 统计口径变化，评优计算需确认是否只计未撤销 |
| `DELETE /students/clear` | 必须带 `confirm:"DELETE_ALL_ROSTER"`；有已关联扣分记录时先返 `409` | 二次确认后加 `unlink_linked:true`；扣分记录不再会被连带删除 |
| `POST /students/batch-import` | `overwrite:true` 必须带 `overwrite_confirm:"REPLACE_ALL_ROSTER"` | 覆盖导入需前端二次确认 |
| 新增 | `POST /deductions/from-report`、`GET /deductions/profile`、`GET /deductions/student-profiles` | — |
| 新增数据 | 表 `inspection_subjects`、`operation_logs`；`inspection_photos` 增 `ai_status`/`report_kind`/`note_text`；`deduction_records` 增 `student_id`/`source_inspection_id`/`source_subject_id`/`revoked_*` | 由 `AutoMigrate` 自动建列建表；历史数据 `student_id=0`、`ai_status=''`，需跑 `cmd/backfill-deduction-student` 回填 |
| Casbin | 撤销了 `/deductions/*` 上两条历史过宽策略（含 `DELETE`），当前为 `(GET)\|(POST)` | 策略持久化在 `casbin_rule`，启动时自动清理旧记录 |

---

## 9. 行为告警（对接前必读）

1. **"HTTP 200 ≠ 成功"是本仓库的普遍现象。** 绝大多数写接口丢弃 `Create`/`Save`/`Updates`/`Delete` 的返回值，因此数据库报错时仍返回 200 与成功文案；`GET` 类接口错误同样被忽略并返回 200 + 空数据。集成方**不能依赖状态码判断落库结果**。
2. **大量 `.(string)` 裸类型断言**（如 `realName.(string)`、`building.(string)`）在 JWT 载荷缺该字段时会 panic ⇒ 500 纯文本，而非 JSON 错误。
3. **硬编码 Limit 造成 `total` 与实际条数不一致**：`/students`(100)、`/minister/schedules`(100)、`/dorm/inspections`(50)、`/publicity/broadcast/news`(50)、`/deductions/student-profiles`(500)、`morning-dorm-reports`(300)。这些接口的 `total` 不能当全量总数用。
4. **姓名即主键的关联方式**遍布排班、出勤、替补、导出（`member_names LIKE '%姓名%'`），会互相误命中、改名即失联；B 组已把**违纪打表**改为 `student_id` 外键，其余模块尚未改造。
5. **导出接口会编造数据**：`/export/daily-duty-csv` 与 `standing-duty-csv` 在姓名匹配不到时写入固定值（`高二(2)班`、`纪检部`、`高二(1)班`），`/export/download-csv` 的"AI 归纳摘要"列实际重复了类别列；`bundle-zip` 的"下周排班表"在窗口为空时**回落到最早的 30 条历史班次**。这些导出文件不能直接作为对外公示依据。
6. **PII 暴露面**：`/tech/ai-configs` 明文密钥、`POST /welfare/gateways` 回读密钥、`/students/room-members` 未限制楼栋、`/export/*` 输出手机号、`/publicity/broadcast/member-push` 无条件发布姓名+分值+理由。
7. **枚举不校验**：`photo_type`、`rule_type`、`action`、`change_type`、`target_department`、`period_type` 均可传任意字符串并被写库。
8. **无幂等与并发保护**：请假审批重复加分；所有福利兑换/扣额度均为"读-改-写"且无事务，可双花。
9. **审计只覆盖 B 组新增的高危写操作**：`deduction.create`、`deduction.create_batch`、`deduction.revoke`、`student_roster.import`、`student_roster.overwrite_import`、`student_roster.clear_all`。**登录、改密、角色变更、通用数据编辑器、导出、请假审批、积分调整均无留痕**。
10. **AI 链路当前不可能产出真实结论**（见 §4.1 告警），且 `GET /tech/db/tables/inspection_photos` 允许手工伪造 `ai_status:"real"` 重新打开这条通道。

---

## 10. 公共响应模型（JSON 键）

| 模型 | 键 |
|---|---|
| `model.User` | `id, username, real_name, phone, role, building, floor, class_name, department, position, total_score, status, created_at, updated_at`（`password_hash` 为 `json:"-"`，不外泄） |
| `model.Student` | `id, student_no, real_name, grade, class_name, building, room_number, bed_number, gender, phone, status, created_at` |
| `model.DeductionRecord` | `id, student_id, building, floor, room_number, student_name, class_name, grade, category, deduct_points, reason, inspector_name, inspector_id, source_inspection_id, source_subject_id, status, revoked_by, revoked_by_name, revoke_reason, revoked_at, created_at` |
| `model.InspectionPhoto` | `id, dorm_manager_id, manager_name, building, room_number, image_url, photo_type, report_kind, note_text, ai_status, vision_ai_output, structured_json, category, deduct_points, severity, status, review_note, created_at, processed_at` |
| `model.InspectionSubject` | `id, inspection_id, raw_name, student_id, class_name, match_status, match_note, converted_deduction_id, created_at` |
| `model.OperationLog` | `id, action, target_type, target_id, operator_id, operator_name, operator_role, detail, request_id, ip, created_at`（**只增不改，无对外读取接口**；⚠️ `request_id` 虽在模型与 JSON 中存在，但 `controller/audit.go` 从不赋值，**落库恒为空串**，不能用于串联请求链路） |
| `model.LeaveRequest` | `id, member_id, member_name, shift_id, shift_info, reason, substitute_id, substitute_name, auto_substitute, substitute_reason, status, minister_id, minister_name, review_comment, reviewed_at, created_at` |
| `model.ScheduleShift` | `id, plan_id, date, week_type, shift_period, building, floor, member_ids_json, member_names, dorm_manager_id, manager_name, status, supervisor_pic, note, created_at` |
| `model.MemberScoreLog` | `id, member_id, member_name, shift_id, change_type, score_change, balance_after, reason, operator_name, created_at` |
| `model.AIConfig` | `id, config_key, display_name, provider, endpoint, api_key(⚠️明文), model_name, system_prompt, temperature, max_tokens, is_enabled, last_tested_at, last_test_result, updated_at` |

---

## 附：本文未覆盖

- 前端各页面实际调用了哪些接口（部分接口**有路由无界面**：`/export/exam-submissions`、`/tech/ai-configs`、`/tech/overview`、`POST|PUT /publicity/broadcast/*`、`/welfare/gateways/:id/exchange`）。
- `operation_logs` 无任何查询接口，目前只能直接查库。
- 未做真实浏览器/客户端联调，所有响应形态来自源码核对与接口实测。
