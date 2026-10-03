# 学管会综合管理系统 · 后端 API 文档

**基准**：`F:\mods\学管会\backend`，Go 1.26 + Gin 1.12 + GORM + SQLite + Casbin。
**接口总数**：**129 个 API 路由**（2026-10-01 逐条静态解析 `cmd/server/main.go` 里全部 `Group()` 与 `GET/POST/PUT/DELETE/PATCH` 注册得出，全部挂在 `/api/v1` 下；另有 `/`、`/static/*`、`/uploads/*` 三个静态挂载不计入）。旧版写的"104 个（按运行中服务的注册日志计数）"已不可复现——服务用 `gin.New()` 建引擎，**不会打印路由表**，只能按源码解析计数。⚠️ 2026-10-02 起又新增：站内信 5 条、`/ext/modules` 与 `/mod/runtimestatus` 各 1 条、P1 插件侧 4 条静态路由（**另加"每装一个插件、启动期再挂 1 条 `/mod/<id>`"的动态部分，不计入任何静态总数**）——**129 这个数已偏低，本轮没有重新逐条推导，要精确数就按同一方法重解析 `main.go`**。
**各分节标题里的数量**（§3 的 15、§5 的 13、§6 的 10+4、§7 的 24）是 2026-09-26 的快照，**未随后续新增接口重新推导**，只作粗略导航用，不要当准确清单。
**核对时间**：初版基于 2026-09-24（P0 制度修复与 B 组数据完整性改造之后）；**2026-09-26 本轮重核 §4.1 宿管上报、§2.4/§4.2 违纪台账、§5 AI 排班对话、§6 技术运维、§7 福利透传**；**2026-10-01 本轮重核 §4.2 打表三道闸门与 §7 宣传/播报全部外部数据源接口**；**2026-10-02 新增 §11 扩展模块（插件契约，含 `/ext/modules` 与 `/mod/runtimestatus` 两条新路由），同日新增的站内信 5 条路由尚未写入本文（见文末附录）**；**2026-10-02 同日 P1 独立进程插件加载器落地，§11 补 §11.5（文件期插件契约）与 §11.6（三条管理接口），本轮只重核 §11，上面那个 129 的总数未重新推导**；**同日 S0 签名准入落地：插件上线多一道"可信开发者签名"门，§11.5 状态表从六个取值变成八个（新增 `unsigned`/`sig_broken`），§11.6 的 `GET /tech/plugins` 多回 `trusted_keys`、`View` 多回 `signed_by`，未签名的插件授权直接 `400`——三处都不新增路由，总数不受影响**。**同日 S2/M3/S3 写动作落地**：新增 §11.7（`POST /mod/<id>/action`），清单契约 `manifest_version` 从 1 升到 **2**（新增 `action` 组件类型与 `actions` 字段），`View` 多回 `actions`，`/ext/modules` 里每个模块可能带 `actions` 与被筛掉的按钮（按操作者对目标接口的权限筛）。**动作路由是启动期按插件挂的动态路由，仍然不进那个静态总数**；stdio 那侧的 `protocol` 仍是 1（理由见《方案》§3 开头）。未列出的章节仍以 09-24 的读码结论为准。

> ⚠️ 请先读 **§8 契约变更** 和 **§9 行为告警**。P0/B 两组改动破坏了若干原有接口契约，按旧文档对接会直接失败。

---

## 1. 通用约定

| 项 | 值 |
|---|---|
| Base URL | `http://<host>:8080/api/v1`（端口取 `PORT`，默认 8080；数据库路径取 `DB_PATH`，默认 `xgh_system.db`） |
| 请求编码 | JSON（`Content-Type: application/json`）；上传为 `multipart/form-data` |
| 认证 | 二者任选：`Authorization: Bearer <JWT>`（APK 用）或 **HttpOnly 会话 Cookie `xgh_session`**（Web 用，登录时由服务端 `Set-Cookie` 下发，`SameSite=Lax`，TLS 下自动加 `Secure`）。HS256，有效期 **7 天** |
| 导出的鉴权方式 | 中间件**只要看到 `Authorization` 头就优先走令牌分支**（`middleware/auth.go:87`）。C1 之后浏览器不再持有令牌，因此 Web 端所有下载/导出（`downloadAuthedFile`）**只带 Cookie，绝不拼 `Bearer`**；一个占位值如 `Bearer undefined` 会让本来有效的会话直接 401 |
| 签名密钥 | 依次取 `JWT_SECRET` 环境变量 → `jwt_secret.key`(0600) → 自动生成随机密钥并落盘。**源码中已无硬编码密钥** |
| 登出 | `POST /auth/logout` 清除会话 Cookie 并留痕（此前仅前端删 localStorage，服务端无吊销） |
| 响应编码 | 一律 UTF-8 JSON；CSV 导出带 UTF-8 BOM（`0xEF 0xBB 0xBF`） |
| 错误格式 | `{"error": "<中文说明>"}`；部分接口附带结构化补充字段（见各节） |
| 分页 | 无统一分页。`GET /deductions` → `page`/`page_size`(≤200)；`GET /tech/db/tables/:table` → `page`/`page_size`(≤100)；`GET /tech/operation-logs` → `page`/`page_size`(≤200)；其余多为**硬编码 Limit**（见 §9） |
| 空集合 | GORM 切片未命中时序列化为 `null` 而非 `[]`，前端必须做空值判断 |
| CORS | 默认**仅同源**。只有列入 `ALLOWED_ORIGINS`（逗号分隔白名单）的 Origin 才获得跨域许可，且不再使用 `*` 叠加凭据 |
| 登录限流 | `POST /auth/login` 与 `/auth/dorm-quick-login` 按"账号+IP"计数，连续失败 5 次锁定 15 分钟，期间返回 `429 {"error","retry_after"}` |
| 高危操作二次验证 | 写扣分、批量打表、撤销扣分、调整他人积分、变更职务/角色必须在请求头带 **`X-Confirm-Password: <当前登录口令>`**，缺失 `400`、口令错 `401` |

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
| `dorm_manager` | `/dorm/*` GET·POST（**该通配是 `…/:id` 与 `…/:id/correct` 两条子路径可达的唯一原因**）；`/schedules/today` GET；`/auth/profile` GET；`/auth/security-settings` PUT；`/auth/logout` POST；`/students/room-members` GET；`/messages` GET·POST + `/messages/*` GET·PUT（§附）；`/ext/*` GET（§11） |
| `member` | `/member/*` `/exam/*` `/welfare/*` GET·POST；`/schedules/*` GET；`/students/room-members` GET；**`/deductions` 与 `/deductions/*` GET·POST**；`/publicity/*` GET·POST·PUT；auth 两项 + `/auth/logout` POST；`/messages` GET·POST + `/messages/*` GET·PUT（§附）；`/ext/*` GET（§11） |
| `minister` | `/minister/*` 全方法；`/schedules/*` `/exam/*` 全方法；`/member/*` GET·POST；`/students/*` GET·POST·DELETE；`/deductions`+`/deductions/*` GET·POST；**`/dorm/inspections` GET（精确路径，不含子路径）**；`/publicity/*` `/welfare/*`；auth 两项 + `/auth/logout` POST；`/messages` GET·POST + `/messages/*` GET·PUT（§附）；`/ext/*` GET（§11） |
| `tech_admin` | `/tech/*` 全方法 + **`/api/v1/*` 全方法**（兜底通配，非硬编码豁免）+ `/mod/*` GET（**显式单列**，见 §11）。§11.6 那三条插件管理路由**不新增策略**，吃的就是 `/tech/*` 这条——"谁能授权插件"因此在 `casbin.go` 里只有一个答案 |
| `viewer_export` | `/export/*` GET·POST；`/schedules/*` GET；`/students/*` GET；**`/deductions` 与 `/deductions/*` GET**；`/dorm/inspections` GET（精确路径，不含子路径）；auth 两项 + `/auth/logout` POST；`/messages`+`/messages/*` **GET·PUT（只收不发，集合路径无 POST）**（§附）；`/ext/*` GET（§11） |

`/auth/logout` 对四个非技术角色都是**后补的**：缺这条时点"退出登录"会被 403 拦下，UI 退回壁纸页但会话 Cookie 仍然有效 —— 公网部署下等于没有退出。

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

### 2.4 台账导出的读者闸门：`requireDeductionLedgerReader`

`controller/deduction_controller.go`，**目前只用于 `GET /deductions/export-csv`**。同样从数据库重读账号（非 JWT 快照），放行条件：

```
账号存在（否则 401）且 status != "disabled"（否则 403）
且满足其一：
  role ∈ {minister, tech_admin, viewer_export}
  或 §2.3 的 HasDeductionAuthority（持打表权的副部长等）
```

- 拒绝响应：`403 {"error":"全校违纪台账导出仅限档案导出岗、部长、技术维护组与持有打表权的副部长；违纪公示请按列表逐页查看","role":"…"}`。
- **`GET /deductions` 故意不设此门**：部员端要读违纪公示，分页列表是公开面；收紧的只是"一次请求带走全校姓名·班级·寝室·违纪事实"的整表导出。二者共同构成"列表可读、整表不可导出"的口径，改动任一侧都要同步另一侧。
- 该接口在写出任何 CSV 字节之前先取数，查询失败返回 `500` 而不是"只有表头的空 CSV"。

---

## 3. 认证、账号与账户安全中心（15 个）

> 2026-10-01 随「账户安全中心」（C3 口令策略 + C4 会话生命周期）整体更新，下列契约以运行中服务实测为准。
> 高危类接口（改密、确认异地登录、解绑动态口令、治理重置与解绑）通过请求头 **`X-Confirm-Password: <登录口令>`** 传原密码，用请求头而非请求体是为了不与处理器的 JSON 绑定争抢同一个 body。

### 3.1 `POST /auth/login` · 公开
- 请求：`username`*、`password`*、`totp_code`（账号已绑定动态口令时**必填**）
- `200`：`{token, user: model.User, must_change_password, new_login_environment}`，同时下发 HttpOnly Cookie `xgh_session`
- 错误：`400` 参数缺失；`401 账号或密码错误`（**账号不存在与密码错误口径已统一，不再可枚举用户名**）；`401 该账号已被停用…`（**已校验 `status`**）；`401 code=totp_required`（已绑定但未填码）/ `401 code=totp_invalid`（码错或已用过，同样计入失败锁定）/ `503 code=totp_unavailable`（密钥解不开等服务端问题，不该由本人重试）；`429` 失败次数达阈值并带 `retry_after`
- 验证码放行规则：RFC 6238 / 30 秒 / 6 位，允许**前后各 1 步**时钟漂移；`totp_last_step` 之前的口令一律拒绝 ⇒ **同一个码用过一次即作废，不能重放**。
- 副作用：登记一行 `user_sessions`（jti/IP/UA）与一条登录历史；已绑定 TOTP 的账号在此通道强制验证码。
- ⚠️ 来源 IP 只有在服务端配了 `TRUSTED_PROXIES` 后才可信，未配置时登录环境判定与异地提示整体退出计分。

### 3.2 `POST /auth/dorm-quick-login` · 公开
- 请求：`phone`*、`building`*、`real_name`*、`totp_code`（同 3.1，`floor` 仍**接收但不使用**）
- `200`：`{message, token, user, must_change_password, new_login_environment}`
- 错误：`400`；`403` 三要素未命中花名册；`403` 楼栋不匹配（带 `preset_building`）；`403` 该手机号已绑定非宿管账号；`403` 花名册记录已绑定其他手机号；`401` 账号已停用；`401 totp_required` / `totp_invalid`、`503 totp_unavailable`（同 3.1）；`429` 限流
- 已闭合的三个洞：楼栋改为规范化后**精确比对**（输入 `"1"` 不再能过 `12号楼`）；无论账号来自 `bound_user_id` 还是手机号匹配，都统一校验 `role==dorm_manager` 与手机号一致；接入与口令登录同一套失败限流。
- 副作用：首次激活自动建号，初始口令**随机生成后即丢弃**（不再是人人可猜的 `123456`），因此该类账号的**首次设密免原密码核验**（三要素通道已核验身份），在审计里注明。

### 3.3 `GET /auth/profile` · JWT · 全角色
- `200`：**裸 `model.User` 对象**（无 `token`/`user` 包装）；`404` 用户不存在
- 安全字段（`token_version`/`password_hash`/`totp_secret_enc` 等）均为 `json:"-"`，不外泄。

### 3.4 `PUT /auth/security-settings` · JWT · 全角色
- 请求（全部可选）：`username`、`real_name`、`phone`、`old_password`、`new_password`
- `200`：`{message, token(重签), user}`
- 错误：`404`；`400` 参数无效；`400 修改手机号或密码时必须输入当前原密码`；`401 当前原密码输入错误，校验未通过`（**只弹提示，不会把人踢下线**）；`409` 用户名被占用；`409` 手机号被其他账号或被宿管花名册占用；`400 code=password_unchanged`（新旧同串）；`400 code=password_policy_rejected`（长度/字符类别/常见口令表，`error` 为首条原因）
- 副作用：**任何一次成功调用都会重签令牌并下发新 Cookie**（当前会话行先被吊销，故客户端必须采用响应中的新 `token`）。改了口令则是 `token_version+1` 且**全部会话下线** ⇒ 其他设备下一次请求得到 `401 code=session_revoked`，本设备凭新令牌继续在线；同时评定 `password_strength`、写 `password_changed_at`。
- 副作用（宿管）：改手机号时会同步回写 `dorm_roster_presets.phone`（三要素以手机号为主键，不同步就会把本人登录改挂）。
- ⚠️ 空字符串＝不修改，**无法清空** `phone`/`real_name`；`role`/`department`/`position`/`total_score`/`status` 在此请求体中被忽略（已在用例中固化为"200 但库里不变"）。

### 3.5 `GET /account/security` · JWT · 全角色
- `200`：`{score, sessions[], logins[], account, ip_trustworthy}`
  - `score`：`{total, level, baseline, meets_baseline, items[], notices[], actionable[]}`；`items[].id ∈ password_set / password_strength / login_environment / second_factor`，`status ∈ ok/warn/fail/unknown`，`scored=false` 时前端显示"暂不判定"且总分按其余项归一
  - `sessions`：活跃会话，最多 50 条，`{id, login_ip, user_agent, login_at, last_seen_at, env_status, current}`
  - `logins`：最近 10 条登录登记（含已下线的）
- 失败：`500` 读取失败；`404` 账号不存在

### 3.6 `POST /account/sessions/revoke` · JWT · 全角色
- 下线除当前 jti 以外的全部会话，**保留本机**。`200 {message, revoked_count}`
- 按会话行吊销，不 bump `token_version`；真要连本机一起退出走 `POST /auth/logout`。

### 3.7 `DELETE /account/sessions/:id` · JWT · 全角色
- `200 {message}`；`400` 编号无效 / 目标是当前会话（提示改用退出登录）；`404` 未找到（查询已按 `user_id` 收窄，**不能越权吊销他人会话**）；已下线的重复调用返回 `200 {already_revoked:true}`

### 3.8 `POST /account/environment/confirm` · JWT · 全角色 · 需 `X-Confirm-Password`
- 把近 30 天内 `env_status=pending_confirm` 的会话记为本人，环境项扣分恢复
- `200 {message, confirmed, current_score}`（`current_score` 为重新评定后的完整 `score` 对象）
- 走 `requireStepUpAccountHygiene`：只重验口令，**不受"尚未改过初始口令"限制**；口令连续错误同样吃 `429` 限流。

### 3.9 `POST /account/totp/setup` · JWT · 全角色
- `200 {secret, otpauth_url, expires_in(900), digits(6), period(30), message}`
- `409` 已绑定（需先解绑）
- ⚠️ 明文密钥只在这一次响应中出现，**不写审计、不写日志**；待绑定密钥存内存、15 分钟过期、且只允许 enable 一次（填错即作废，需重新 setup）。**服务端不产出图片**：二维码由前端用本地化的 `static/vendor/qrcode-generator-1.4.4.js` 把 `otpauth_url` 画在 canvas 上，接口契约不变，非浏览器端（APK / Win7 客户端）可忽略该串自行处理。

### 3.10 `POST /account/totp/enable` · JWT · 全角色
- 请求：`code`*（验证器当前 6 位）
- `200 {message}`；`400` 未填或格式不正确；`401` 验证码校验未通过（**失败即作废本次待绑定密钥**，错码者不该拿同一个密钥反复试探）；`409` 已绑定 / 绑定会话已过期
- 生效范围：**两条登录通道**（3.1 与 3.2）此后都要求验证码；密钥经 secretbox 加密为 `enc:v1:` 前缀入库。

### 3.11 `POST /account/totp/disable` · JWT · 全角色 · 需 `X-Confirm-Password`
- `200 {message}`；`404` 尚未绑定；`401` 口令错误 / `429` 限流
- 清空 `totp_secret_enc` 与 `totp_last_step`，审计只记"已解绑动态口令二次验证"。

### 3.12 `GET /tech/account-governance` · JWT · 仅 `tech_admin`
- `200 {summary, accounts[], ip_trustworthy, transport_https, baseline_note, not_returned, limit}`
  - `summary`：`total_accounts / initial_password_accounts / no_second_factor_accounts / privileged_below_baseline`
  - `accounts[]`：按"高权 × 低分"优先排序，含 `score/baseline/password_set/totp_enabled/last_login_ip/pending_env_logins`
- 非 tech_admin 一律 `403`；接口不返回任何口令字段。

### 3.13 `POST /tech/users/:id/reset-password` · JWT · 仅 `tech_admin` · 需 `X-Confirm-Password`
- `200 {message, initial_password, notice}` —— **新口令只在这一次响应里出现**
- 副作用：随机口令覆盖目标、`password_changed_at` 置空（回到"待本人设密"，高危写与强制改密提示随之生效）、`token_version+1` 且全部会话下线。
- 审计明细只记"已重置"，不含口令。

### 3.14 `POST /tech/users/:id/totp-unbind` · JWT · 仅 `tech_admin` · 需 `X-Confirm-Password`
- `200 {message}`；`404` 目标不存在 / 未绑定；`400` 编号无效
- 供本人验证器遗失时救急；审计只记"已解绑"。

### 3.15 `POST /auth/logout` · JWT · 全角色
- 吊销当前会话行 + 清 Cookie + 留痕（只清 Cookie 的话被偷走的令牌在服务端依然有效）。
- ⚠️ 各角色的 Casbin 策略必须齐全，缺策略会让界面回到壁纸页但**会话仍然有效**（2026-09-25 已补齐并实测）。

### 3.16 认证类错误码契约（对接方按 `code` 分支，不要按中文文案匹配）
| HTTP | `code` | 含义与前端应有的反应 |
|---|---|---|
| `401` | `session_missing` | 从未持有有效会话 ⇒ 回登录页 |
| `401` | `session_expired` | 令牌过期 ⇒ 回登录页 |
| `401` | `session_revoked` | 会话被吊销（改密/重置/手动下线） ⇒ 提示后回登录页 |
| `401` | `account_disabled` | 账号被停用 ⇒ 提示后回登录页 |
| `401` | `totp_required` / `totp_invalid` | **仅出现在两个登录接口** ⇒ 停留在登录页要码/报错，不当作会话失效 |
| `503` | `totp_unavailable` | 服务端解不开密钥 ⇒ 提示联系技术维护组，不要让本人反复重试 |
| `403` | `password_change_required` | 仍在使用初始口令 ⇒ 弹强制改密框（**不是掉线，不要清会话**） |
| `400` | `password_unchanged` / `password_policy_rejected` | 口令被拒 ⇒ 停留在表单并显示 `error` |

> ⚠️ **只有上表四个 `session_*` / `account_disabled` 的 401 才该登出**。其余 401（登录口令错、step-up 确认口令错）只提示、不清会话。历史上前端把"任何 401"当作会话失效，导致二次确认输错口令直接把人踢下线，已在 `static/app.js` 的 `request()` 中修正。

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
| `note_text` | string | 否 | **现场描述文字**；换行归一，**按字符截断至 2000**。仅当 `subject_names` 留空时才从中兜底拆名（老安卓端兼容），宿管前端已用独立「现场描述」框承载 |
| `subject_names` | string | 否 | **显式名单，宽松拆分**：条目为 2~12 个汉字/字母/间隔号`·`/下划线`_`（至少 2 个汉字或字母位，如 `买买提·艾力`、`LiHua`、`李_华`），分隔符 `、，,;；\|／/` 与空白；数字/其他标点仍拒绝。留空且 `report_kind=text` 才从 `note_text` 兜底严格拆分（2~6 纯汉字）。**显式填了名单但拆不出有效条目 ⇒ 400**（不再静默丢弃） |
| `image` | file | **`report_kind != "text"` 时必填** | 落盘 `./uploads/<uuid8>_<客户端文件名>` |

- `200`：`{message, record: model.InspectionPhoto, ai_status, vision_analysis, structured_result|null, subject_total, subject_matched, subject_unmatched}`
- `structured_result` 键：`category, severity, deduct_points, summary, tags, action_advice`
- 错误：`400`（无图 / 无寝室号）；`500`（建目录、存盘、事务失败）
- ⚠️ **`ai_status` 三态**（`real`/`disabled`/`failed`，空串为迁移前历史数据）：自 D-1 起 `pkg/ai/client.go` 的 `normalizeVisionImage` 会把 `/uploads/...` 本地路径**读盘转 base64** 再送上游，因此配了可用密钥 ⇒ `real`，未启用 ⇒ `disabled`，已配置但上游报错/超时 ⇒ `failed`。**只有 `real` 才是模型真实产出**；`disabled`/`failed` 时 `category/deduct_points/severity` 保持零值，`status` 停在 `uploaded`，必须人工看图录入。
- ⚠️ 名单被 `service.MaxSubjectsPerReport = 50` 截断（`report_helpers.go` 里的 60 上限不可达）；文件名取自客户端（D-4 已加体积与扩展名上限）；上传本身不写 `operation_logs`。

#### `GET /dorm/inspections` · JWT · `dorm_manager` `minister` `viewer_export` `tech_admin`
- 查询：`category`、`severity`
- `200`：`{total, items:[model.InspectionPhoto]}`，**硬编码 `Limit(50)`**
- ⚠️ 行级隔离只对 `dorm_manager` 生效（按 `building LIKE`）；部长与导出人员可看全校所有楼栋及宿管姓名。

#### `GET /dorm/inspections/:id` · JWT · 点卡片看详情
- 路径：`id` 非数字或 `0` ⇒ `400 记录编号无效`
- `200`：`{record: model.InspectionPhoto, subjects:[model.InspectionSubject], subject_total, linked_deductions:[{id, student_name, class_name, category, deduct_points, status}], structured}`
- `structured` 是 `structured_json` 的解析结果；**空串、半截 JSON、非对象 JSON 一律回 `null`**，解析失败不影响本接口成功。
- `linked_deductions` 按 `source_inspection_id` 取，**含已撤销条目**（靠 `status` 区分），只回带核对所需的 6 个字段，不含 `reason` 等原文。
- 权限：`dorm_manager` 走 Casbin 的 `/api/v1/dorm/*` 通配，**服务端按与列表逐字相同的 `building LIKE` 规则再过滤一次**；`tech_admin` 走兜底通配可跨栋。
- ⚠️ **部长与信息查看下载岗拿不到详情**：他们的策略是精确路径 `/api/v1/dorm/inspections`，不含子路径 ⇒ 能看列表、点详情会 403。宿管终端是唯一入口，若日后在部长端复用需先补策略。
- ⚠️ "记录不存在"与"不属于本楼栋"**合并为同一个 404 文案**，不得为了提示友好而分开，否则详情接口会变成跨楼栋的记录探测面。

#### `POST /dorm/inspections/:id/correct` · JWT · 上报者本人（`tech_admin` 例外）
- 请求：`vision_analysis`、`category`、`severity`、`deduct_points`、`summary`、`action_advice`（均为指针，可只传改动项）、`reason`*
- `200`：`{message, record}`；`400`（编号无效 / 缺 `reason` / `reason` 超 500 字 / 严重度非枚举 / **建议扣分不在 0~30**（此处 `0` 合法）/ **未检测到任何改动**）；`403` 非本人；`404` 记录不存在；`409` 该上报**已转打表**
- 边界一：**`ai_status` 一个字都不改**。人工改写结论不得冒充模型产出，否则打表面板的 `aiVerified` 判定（§4.2）就被绕过。
- 边界二：每次纠正把带时间与差异的说明**追加进 `review_note`** 并写 `operation_logs`；已转打表的记录必须走"撤销打表"流程回来改。
- ⚠️ **空串等于"不改"**：指针字段虽可省略，但传空串会被 `applyStr` 当作未修改静默跳过 ⇒ **无法把某个结论清空**，只能覆盖为非空文本。

#### `POST /dorm/inspections/:id/subjects` · JWT · 上报者本人（`tech_admin` 例外）
上传后补记名单。请求 `{"names": "王五、LiHua"}`。
- 名单走**宽松拆分**（同 `upload-photo` 的 `subject_names`：2~12 字符汉字/字母/间隔号，数字标点拒绝，上限 50）；只增不删、按原始姓名去重（库内 + 批内），每条逐一过 `MatchSubjectInRoom` 楼栋+寝室名册核对。
- `200`：`{message, added, duplicates, matched, unmatched, subjects, subject_total}`；`400` 空/全无效（前者提示"请填写名单"，后者提示有效姓名规则）；`403` 非本人；`404` 不存在；`409` **已转打表**（名单快照已核对完毕，补记需先撤销打表）。
- `status`/`ai_status` 不动；`review_note` 追加 `[时间] 姓名 补报名单： +名单`，写审计 `dorm.inspection.subjects.append`。

### 4.2 副部长核对与打表

#### `GET /deductions/morning-dorm-reports` · §2.3 严格门禁
- 查询：`scope`（非 `all` 一律按 `today`）
- `200`：`{total, items:[MorningDormReportDTO], today, scope, period_title}`
- DTO：`id, building, floor, room_number, manager_name, photo_type, report_kind, note_text, image_url, ai_status, submitted_text, category, severity, deduct_points, created_at, is_reported, is_deducted, subject_total, subjects[]`
- `subjects[]`：`id, raw_name, student_id, class_name, match_status, match_note, converted_deduction_id`
- 行为：`ai_status != "real"` 时**强制清零** `category/severity/deduct_points`，`submitted_text` 回落为 `note_text` 或固定人工核对提示；`is_deducted` 判定为该上报存在未撤销的关联扣分（或 `status='converted'`）。
- ⚠️ 一次加载最多 300 条上报 + 其全部名单 + **整张 `dorm_roster_presets`**；楼层按宿管姓名映射，同名宿管后者覆盖前者。

#### `POST /deductions` · §2.3 严格门禁 · **需 `X-Confirm-Password`**
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

#### `POST /deductions/from-report` · §2.3 严格门禁 · **需 `X-Confirm-Password`**
- 请求：`inspection_id`*、`subject_ids`*(`[]uint`)、`floor`、`category`*、`deduct_points`*、`reason`*、`disposition`（"如何处理"，可空）
- `200`：`{message, count, records:[model.DeductionRecord]}`
- `400`：`上报记录不存在` / `勾选的名单条目与该上报不匹配` / `{"error":"名单中的【X】无法打表：…","subject":"X"}`
- `409`：`{error, existing_deduction}`（预检）或 `{error}`（事务内检出重复）
- 行为：**全成全败**——任一名次未通过名册核对即整体中止；楼栋与寝室号一律继承上报记录，客户端不能覆盖；`floor` 缺省按寝室号首位推算（`302 → 3F`）。`disposition` 留空时**自动取这次上报里 AI 已生成的处置建议**（`ai_status != real` 时没有建议可取，就留空由人工补），这一栏就是班主任看到的那句"如何处理"。审计动作 `deduction.create_batch`。

#### `POST /deductions/:id/revoke` · §2.3 严格门禁 · **需 `X-Confirm-Password`**
- 请求：`{reason: string}` — **无 `binding` 标签**，但去空白后为空 ⇒ `400 撤销打表记录必须填写理由…`
- `200`：`{message, record}`（record 为内存中已置为撤销态的对象）
- `404` 记录不存在；`409` `{error, revoked_by, revoked_at}` 已撤销过
- 行为：**软撤销**——`status='revoked'` + `revoked_by/revoked_by_name/revoke_reason/revoked_at`，原始分值与时间不变；回退 `inspection_subjects.converted_deduction_id`；该上报无剩余有效扣分时把 `inspection_photos.status` 退回 `ai_analyzed`（因而可重新打表）。审计动作 `deduction.revoke`。

**打表即生效，没有审核态。** `deduction_records` 里没有 `pending_review/approved/rejected` 状态列、也没有审核人字段；`status` 只有 `confirmed`/`revoked` 两值。2026-09-27 手稿要求的"**年级主任审核 → 班主任执行**"环节**至今未实现**，且系统现有 5 个 role 里没有年级主任与班主任两种身份（见 `学管会系统_工作流改造计划.md`，那是计划不是现状）。同一份手稿还要求名册退出主链路（身份键改为姓名+楼栋+寝室+床位），而当前 `resolveStudent` 的防冒名核对仍依赖 `students` 表——**这两处方向性差异在动打表链路前必须先对齐**。

#### `GET /deductions` · JWT · Casbin 放行 `member` `minister` `tech_admin`
- 查询：`floor` `room` `name` `class` `building`(LIKE)、`category`(精确)、`q`(跨 6 字段模糊)、`status`(`confirmed`/`revoked`)、`date_from`/`date_to`(`yyyy-MM-dd`，含首末两天)、`page`、`page_size`(≤200)
- `200`：`{total, page, page_size, total_deduct_sum, unlinked_in_page, status_filter, items:[model.DeductionRecord]}`
- ⚠️ **本接口没有 §2.3 门禁**：任一部门的部员/部长都能读全校违纪台账。`total_deduct_sum` 排除 `revoked`，但 `items` **包含** `revoked`；`status` 传非枚举值被静默忽略（返回全量）。

#### `GET /deductions/export-csv` · JWT · §2.4 读者闸门（`viewer_export` `minister` `tech_admin` 及持打表权者）
- 查询参数与 `GET /deductions` 完全相同（无分页）：`building` `floor` `room` `name` `class` `category` `status` `q`，**2026-10-01 新增 `date_from` / `date_to`**（`yyyy-MM-dd`，按记录时间，**含首末两天**；`date_to` 取当天 23:59:59.999999999；格式不合法按未填处理，绝不静默少导数据）
- 响应：`text/csv; charset=utf-8`，`Content-Disposition: attachment; filename="学管会打表扣分明细_YYYYMMDD_HHMMSS_mmm.csv"`（**未做 RFC 5987 编码**；毫秒后缀为 2026-10-01 补，解决同一秒两次导出互相覆盖），UTF-8 BOM
- 列（**17 列**）：`打表流水号, 所属楼栋, 楼层, 寝室房间号, 学生班级, 违纪学生姓名, 违纪行为类别, 扣除分值(-N), 违纪具体事由详情, 如何处理, 打表记录人, 记录时间, 存底状态, 撤销人, 撤销时间, 撤销理由, 名册关联`
- 审计：成功导出写一条 `deduction.export_csv`，明细为 `导出违纪台账 CSV：N 条；筛选：<楼栋/楼层/寝室/班级/类别/状态/时间>`。**刻意不记 `name` 与 `q`**——否则审计本身会变成第二份违纪名单。
- 前端入口：档案导出岗/部长/技术维护组在【档案查询与导出】分栏的"违纪台账导出"专区（`downloadViolationRosterCSV`），持打表权的副部长另有打表面板内旧按钮。

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
| `POST /minister/ai-schedule/chat` | minister tech | `messages[{role,content}]`、`image_url`、`plan_date` | 成功 `{reply, suggested_shifts[], real_members[], server_date, ai_status:"real"}`；失败 `503`(disabled)/`400`/`502`(failed) 且**不返回编造内容** |
| `POST /minister/ai-schedule/apply` | minister tech | `plan_title`、`shifts[]`(required) | `{message, plan_id, shifts_count}` |

要点（都会影响对接）：
- **`member/leave` 与 `my-shifts` 按姓名匹配班次**（`member_names LIKE`），不是按 ID ⇒ 改名即失联、互为子串的姓名会互相看到对方班次。
- **请假/审批不校验归属**：任一 `member` 可对任意 `shift_id` 提请假；任一 `minister` 可审批全校请假，且响应含 `review_comment`、替班人手机号等。
- **`action` 与 `rule_type` 都不做枚举校验**：`action="foo"` 会被原样写进 `leave.status`；`rule_type` 只有 `"weekday"` 真正生效（跳过周末），其余值一律"每天排"。
- **审批通过 + 有替班**的副作用：改写 `schedule_shifts.member_names`（子串替换，`张三` 会命中 `张三丰`）、追加 `note`、`users.total_score += 3`、写一条 `duty_substitute` 流水。**重复审批会重复加分**（非幂等）。
- **`change_type` 被丢弃**：`scores/adjust` 落库恒为 `manual_adjust`；`score_change` 为 int 且 required ⇒ **传 0 会被拒**；分值可正可负无上限，`total_score` 可变负。
- **`PromoteMember` 只改 `position` 字符串，不改 `role`**，而 Casbin 只认 `role` ⇒ 升职本身不产生任何 API 权限（打表能力由 §2.3 的部门+职务判定提供）；其部门归属检查是**双向 `Contains`**，操作者部门为空时检查直接形同不存在。
- **`ai-schedule/chat` 已接真实模型**（原"恒定返回未来 5 天 × 4 栋楼 = 20 条 / 楼栋硬编码 / `image_url` 编造一段 OCR"全部废止）：`image_url` 走视觉引擎读图转写，正文走文本引擎，楼栋与部员候选来自数据库实际数据。引擎未配置 ⇒ `503 {"ai_status":"disabled"}`，输入不可用 ⇒ `400`，上游报错或返回不可解析 ⇒ `502 {"ai_status":"failed"}`。**只要不是 `real` 就不得当作模型结论展示**，前端按状态分别提示。`apply` 侧 `shifts: []` 仍能通过 `binding:"required"`（validator 对 slice 只判非 nil），随后 `req.Shifts[0]` **越界 panic ⇒ 500 空响应体**（2026-09-26 复测仍在）。
- `GET /minister/schedules` 硬 `Limit(100)`，`total` 即截断后的条数。
- `week_duty_status` 的"红色旷工"只有当 `schedule_shifts.status` 字面为 `missed` 时才出现，而**代码里没有任何地方写这个值**；`desc := s.Date[5:]` 在 `date` 短于 6 字符时会 panic。

---

## 6. 技术运维（10 个）与后台数据编辑器（4 个）

| 方法与路径 | 角色 | 说明 |
|---|---|---|
| `GET /tech/ai-configs` | tech | 返回 `[]model.AIConfig`；`api_key` 为 `json:"-"` **不再序列化**（入库前 AES-GCM 封装），界面凭 `has_key`/`key_mask` 显示状态 |
| `PUT /tech/ai-configs/:id` | tech | 请求体按 `model.AIConfig` 绑定，仅复制 8 个字段；`api_key:""` = 保留原密钥；`Save` 全列覆盖且错误忽略 ⇒ 失败也报"已更新并生效" |
| `POST /tech/ai-playground/test` | tech | `{config_key}*, input_text, image_url, photo_type}` → `{status:"success"\|"failed", engine_configured, duration_ms, engine, model_name, result, error}`；**AI 失败仍返 200**；成功时 `error` 字段是字符串 `"<nil>"`；会写 `last_tested_at/last_test_result`；`image_url` 支持服务器本地 `/uploads/...`（读盘转 base64）、`data:image` 与 http(s) 三种形态 |
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
| `GET /publicity/images`、`GET /publicity/gallery/random-images` | member minister tech | **2026-10-01 重写为真实上游**：后端调用栗次元随机图 API `https://t.alcy.cc/json?<分类>=<条数>`，把图取回并落到本地缓存目录 `./publicity-cache/`，浏览器只访问本站同源地址（访客 IP 不外泄，与主页壁纸本地化同一口径）。参数 `tag`（白名单 11 个分类，非法则 400 并返回 `allowed`）、`limit`（默认 6，夹在 1–12）；响应含 `upstream`（`online`/`cache`）、`notice`、`categories`；上游不可达时回落到"最近一次成功且已在本地的"批次，不编造数据。环境变量 `PUBLICITY_IMAGE_BASE`（换镜像源）、`PUBLICITY_IMAGE_CACHE_DIR`。⚠️ 上游无内容分级，随机结果可能不适合校园场景；`acg` 分类返回 mp4 视频，已从白名单剔除 |
| `GET /publicity/images/categories` | member minister tech | 返回可选分类表（`key/name/note`），供前端渲染标签胶囊，界面不再硬编码标签；不含 `acg` |
| `GET /publicity/images/file/:name` | member minister tech | 文件名必须匹配 `^[0-9a-f]{64}\.(webp\|jpe?g\|png\|gif)$`（链接 sha256 + 原扩展名），否则 404 ⇒ 既不能路径穿越，也列不出目录 |
| `POST /publicity/broadcast/ai-script` | member minister tech | **2026-10-01 新增：今日新闻早知道 AI 讲稿**。后端按「世界局势 / 国内大事 / 科技新闻」三路调用 Tavily（`topic=news`、`time_range=day`，每路 5 条），天气读 `BroadcastWeatherCache` 的**当日缓存**（一天只打一次 uapis.cn）；**开场白与天气句由 Go 拼，模型只写三条正文**，因此不会出现编造的天气。正文超出 60–260 字判不合规并 502；三路检索全挂直接 502 不出稿。**只读不写**：响应 `saved:false`，不写 `broadcast_news_items`。响应含 `news_refs`（出处，供播出前人工核对）、`warnings`、`news_words`。环境变量 `FEED_SEARCH_ENDPOINT`/`FEED_WEATHER_ENDPOINT` 可整体换端点，但**非 https 且非本机回环时拒绝携带密钥发出请求** |
| `GET /publicity/broadcast/feed-config` | member minister tech | 返回天气城市、检索开关与 `has_key`/`key_mask`；**密钥不回传浏览器** |
| `PUT /publicity/broadcast/feed-config` | **tech_admin** | 改天气城市/开关/密钥。`api_key` 留空 = 保留原值，显式 `clear_key:true` 才清空；Casbin 对 `/publicity/*` 连 PUT 一起开给了部员，**所以"仅技术维护组"是在 controller 里判的**，漏这条就是任意部员能改全校播报数据源。审计只记城市/开关/密钥状态，不记密钥 |
| `GET /welfare/gateways` | member minister tech | 返回 `key_mask`（前4****后4），原始密钥不外泄；⚠️ 但 `user_remain_quota` 取的是**旧版额度表**，与实际扣减的不是同一份 |
| `POST /welfare/gateways` | member minister tech | 密钥**不回传浏览器**（`TechWelfareGateway.APIKey` 为 `json:"-"`，入库前 AES-GCM 封装），响应只给 `has_key` / `key_mask`；`api_key:""` 表示保留原密钥；`base_url` 受 D-2 白名单校验。⚠️ `owner_id=0` 的行仍任意部员可改 |
| `POST /welfare/gateways/:id/probe-models` | member minister tech | 无归属校验 ⇒ 可用他人密钥发起外呼；**探测失败会编造一份模型清单并写库**，同时仍返回"🎉 成功识别到 N 个可用模型"；非 200 分支泄漏 `resp.Body` |
| `POST /welfare/gateways/:id/exchange` | member minister tech | 路径 `:id` 根本不读，网关取自请求体 `gateway_id`；三处写**无事务** ⇒ 可并发双花；错误全忽略 ⇒ 恒返"兑换成功" |
| `POST /welfare/exchange-model` | member minister tech | `is_enabled` 先于余额校验；定价查询不带部门过滤 ⇒ 可买他部（或攻击者自定价）的低价包；同样三处无事务写 |
| `POST /welfare/chat-relay` | member minister tech | **真实透传**：先做前置校验，网关不可用/未启用 ⇒ `503`，上游报错或非 200 ⇒ `502`，**两种失败都不扣额度、不返回编造应答**；成功时才以条件化原子 UPDATE 扣一次额度（余额在 SQL 条件里判定，杜绝并发双花），响应 `ai_status` 为 `real`。出站地址受 D-2 白名单约束，不再是用户填写的任意 `base_url` |
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

### C 组（登录与会话）与 D 组（安全基线）追加的契约变更

| 接口 / 行为 | 变更 | 迁移方式 |
|---|---|---|
| 所有受保护接口 | 新增接受 HttpOnly Cookie `xgh_session`；`Authorization: Bearer` 仍有效（APK） | Web 端去掉 localStorage token，改用同源 fetch |
| `POST /auth/login` | 新增 `POST /auth/logout`；连续失败 5 次返回 **429**；"账号不存在"与"密码错误"统一为 `账号或密码错误`；**`status=disabled` 的账号不再能登录**（原先错误文案说不禁用但其实不禁） | 处理 400/401/429 三种；前端登出调用服务端 |
| `POST /auth/dorm-quick-login` | 楼栋改**规范化后精确比对**（`"1"` 不再匹配 `12号楼`，`"12栋"` 可匹配）；命中非宿管账号一律 403；同样有 429 限流 | 输入完整楼栋名；教师账号勿录入宿管花名册 |
| `PUT /auth/security-settings` | 改手机号或密码**必须带 `old_password`**；新手机号查重；宿管改手机号自动同步其花名册绑定 | 前端补原密码输入 |
| `POST /deductions`、`/deductions/from-report`、`/deductions/:id/revoke`、`/minister/scores/adjust`、`/minister/members/promote` | 必须带请求头 **`X-Confirm-Password`** | 交互中索取登录口令 |
| `POST /tech/db/tables/users` | 携带 `role` → **403**，新建账号角色恒为 `member` | 建号后改用 `POST /tech/users/:id/role` |
| `PUT /tech/db/tables/users/:id` | 载荷中的 `role`、`password_hash` 被静默剥离（原先可改写任意账号密码）；`Updates` 错误不再被吞 | 角色走新接口；改密走 `security-settings` |
| 新增（只读） | `GET /tech/operation-logs`（仅 tech_admin）、`POST /tech/users/:id/role`（需口令+理由） | — |
| 跨域 | 默认仅同源；需跨域要在 `ALLOWED_ORIGINS` 白名单中列出来源 | APK 若为独立 Origin 需显式配置 |
| `POST /dorm/upload-photo` | 图片 >8MB 或非 `image/*` → 400；`MaxMultipartMemory` 降到 8MB | 前端压缩/校验 |
| `GET /students/room-members` | 非 `dorm_manager`/`tech_admin` 角色的 `phone` 脱敏为 `138****5678` | 需要明文的角色不要走此接口 |
| `POST /welfare/gateways`、`/gateways/:id/probe-models`、`/welfare/chat-relay` | `base_url` 仅允许 http/https 且不得指向回环/私网/链路本地地址；保存时与发请求前各校验一次 → **400** | 清理历史上已入库的内网地址 |
| `GET /deductions/export-csv` | 新增 §2.4 读者闸门：此前**任一登录用户**可整表导出全校台账，现仅 `viewer_export`/`minister`/`tech_admin` 及持打表权者可导出，其余 `403` | 违纪公示改用分页列表 `GET /deductions`（该接口未收紧） |
| `GET /dorm/inspections/:id`、`POST /dorm/inspections/:id/correct`（新增） | 宿管终端点卡片看详情、AI 结论人工纠正 | ⚠️ 部长的 Casbin 策略是精确路径 `/dorm/inspections`，**不含子路径** ⇒ 部长/导出岗可读列表但访问这两条会 403 |
| 环境变量 | 新增 `JWT_SECRET`、`DB_PATH`、`ALLOWED_ORIGINS`；`JWT_SECRET` 缺省时自动生成并写入 `jwt_secret.key`(0600) | 生产建议显式设置 `JWT_SECRET` |

### 账户安全中心（C3 口令策略 + C4 会话生命周期，2026-10-01）追加的契约变更

| 接口 / 行为 | 变更 | 迁移方式 |
|---|---|---|
| 所有受保护接口 | 会话从"令牌自证"变为**服务端状态**：每次请求按 `jti` 比对 `user_sessions` 并核对 `tv`；被吊销/停用分别返回 `401 code=session_revoked` / `account_disabled` | ⚠️ `tv` 字段上线时**所有存量令牌一次性失效**（等同全量重登），属预期行为，上线前要通知使用方 |
| `POST /auth/login`、`POST /auth/dorm-quick-login` | 请求新增可选 `totp_code`；响应新增 `must_change_password`、`new_login_environment`；已绑定动态口令的账号缺码即 `401 totp_required` | 登录页要能出示验证码输入框；按 `must_change_password` 引导改密 |
| 全部高危写接口（§2.3 与 `/deductions*`、`/minister/*`、`/tech/users/:id/role`） | **仍在使用初始口令的账号返回 `403 code=password_change_required`**（此前该状态不存在） | 前端识别该码弹强制改密框，**不要当成越权也不要登出** |
| `PUT /auth/security-settings` | 新增 `400 code=password_policy_rejected`（长度/字符类别/常见口令表）、`400 code=password_unchanged`、`409` 手机号被账号或被花名册占用；改密即 `tv+1` **吊销全部会话**（此前"旧 token 仍有效满 7 天"） | 表单需展示拒绝原因；改密后用响应里的新 `token`/Cookie 覆盖本地 |
| 新增 11 个端点 | `GET /account/security`、`POST /account/sessions/revoke`、`DELETE /account/sessions/:id`、`POST /account/environment/confirm`、`POST /account/totp/{setup,enable,disable}`、`GET /tech/account-governance`、`POST /tech/users/:id/{reset-password,totp-unbind}`（详见 §3.5–§3.14） | 自助面全角色可用（Casbin 已放行）；治理面仅 `tech_admin`，其余 `403` |
| `POST/PUT /tech/db/tables/users[/:id]` | 通用编辑器载荷中的 7 个服务端自持字段被**静默剥离**：`password_changed_at`、`password_strength`、`totp_secret_enc`、`totp_last_step`、`token_version`、`last_login_ip`、`last_login_at`（加上此前的 `role`、`password_hash`） | 这些状态只能通过安全中心与治理端点改变；编辑器写操作现已补审计，明细**只记字段名不记取值** |
| 环境变量 | 新增 `CRYPTO_SECRET`（secretbox 主密钥，缺省落盘 `crypto_secret.key`(0600)）、`COOKIE_SECURE`、`TRUSTED_PROXIES` | ⚠️ 改了 `CRYPTO_SECRET` 会让已封存的 TOTP 密钥解不开（表现为登录 `503 totp_unavailable`），迁移密钥需另行处理；未配 `TRUSTED_PROXIES` 时登录环境判定与异地提示自动退出计分 |
| 数据表 | 新增 `user_sessions`（jti/UA/IP/env_status/login_at/last_seen_at/revoked_at）；登录历史复用该表 | 由 `AutoMigrate` 建表；上线前已有会话不在表内，首次登录起才登记 |

### 2026-10-01（外部数据源：素材工坊 + 播音 AI 讲稿）追加的契约变更

| 接口 / 行为 | 变更 | 迁移方式 |
|---|---|---|
| `GET /publicity/images`、`/publicity/gallery/random-images` | **响应字段改名**：图片地址由 `image_url` 改为 `url`（另有 `source_url`/`tag_name`/`ext`/`bytes`），旧的五个硬编码 Unsplash 图片池与 `source_api` 标签**整体删除**，改为真实上游栗次元 + 后端代理 + 本地缓存 | ⚠️ 按 `image_url` 取值的旧前端会拿到 `undefined` ⇒ 全裂图（本仓库前端已同步改掉）；新增的 `upstream`(`online`/`cache`) 与 `notice` 要一并渲染，降级时界面必须说明原因 |
| 新增 7 个端点 | `GET /publicity/images/categories`、`GET /publicity/images/file/:name`、`POST /publicity/broadcast/ai-script`、`GET/PUT /publicity/broadcast/feed-config`（另有 `GET /publicity/images` 与 `gallery/random-images` 两个既有路径改实现） | 分类胶囊改由 `categories` 渲染，界面不要再硬编码标签；文件路由只认 `^[0-9a-f]{64}\.(webp\|jpe?g\|png\|gif)$` |
| `POST /publicity/broadcast/ai-script` | **不读 `AIConfig.SystemPrompt`**：讲稿的格式与字数约束写死在处理器里（开场白与天气句由 Go 拼，模型只写三条正文）。三路检索全挂 `502 news_upstream_failed`、正文越界 `502 ai_output_off_spec`、天气失败只降级不出假天气 | 想让文本引擎的全局风格生效，需要显式改成"系统提示词 + 格式硬约束"两段拼接，目前**故意不拼**；稿子**不落库**（响应 `saved:false`），要存档由前端复制后另行录入 |
| 新增按日缓存 | 表 `broadcast_weather_caches`，`date` 唯一索引 ⇒ **天气上游每天最多被调用一次**，第二次生成直接读当天行并在 `warnings` 里说明 | 换城市不会自动重取（缓存按日期不按城市），当天要改城市需手工删该行 |
| `PUT /publicity/broadcast/feed-config` | 权限**在 controller 里判 `tech_admin`**，不在 Casbin（后者对 `/publicity/*` 连 PUT 一起开给了部员）；`api_key` 留空 = 保留原值，显式 `clear_key:true` 才清空 | 部员调用得 `403`；审计 `broadcast.feed_config` 只记城市/开关/密钥状态 |
| `GET /deductions/export-csv` | 新增 `date_from`/`date_to`（含首末两天，格式非法按未填）；文件名加毫秒后缀；列扩到 **17 列**（含"如何处理"与"名册关联"）；成功导出写审计 `deduction.export_csv` | 审计**不记 `name` 与 `q`**，别指望从审计里还原出是谁查了哪个学生 |
| 环境变量 | 新增 `PUBLICITY_IMAGE_BASE`、`PUBLICITY_IMAGE_CACHE_DIR`、`FEED_SEARCH_ENDPOINT`、`FEED_WEATHER_ENDPOINT` | 只用于整体换镜像源/测试。⚠️ 检索请求带 `Authorization: Bearer`，**端点非 https 且非本机回环时后端直接拒发**；消毒规则要求图片与 JSON 同主机，跨域镜像会被整批丢弃 |
| 数据表 | 新增 `publicity_assets` 不参与本轮任何接口（素材工坊不入库）、`broadcast_feed_configs`、`broadcast_weather_caches` | `AutoMigrate` 建表；`publicity-cache/` 是运行目录，**不在发布包内**且已进 `.gitignore`，需保证可写 |

---

## 9. 行为告警（对接前必读）

1. **"HTTP 200 ≠ 成功"是本仓库的普遍现象。** 绝大多数写接口丢弃 `Create`/`Save`/`Updates`/`Delete` 的返回值，因此数据库报错时仍返回 200 与成功文案；`GET` 类接口错误同样被忽略并返回 200 + 空数据。集成方**不能依赖状态码判断落库结果**。
2. **大量 `.(string)` 裸类型断言**（如 `realName.(string)`、`building.(string)`）在 JWT 载荷缺该字段时会 panic ⇒ 500 纯文本，而非 JSON 错误。
3. **硬编码 Limit 造成 `total` 与实际条数不一致**：`/students`(100)、`/minister/schedules`(100)、`/dorm/inspections`(50)、`/publicity/broadcast/news`(50)、`/deductions/student-profiles`(500)、`morning-dorm-reports`(300)。这些接口的 `total` 不能当全量总数用。
4. **姓名即主键的关联方式**遍布排班、出勤、替补、导出（`member_names LIKE '%姓名%'`），会互相误命中、改名即失联；B 组已把**违纪打表**改为 `student_id` 外键，其余模块尚未改造。
5. **导出接口会编造数据**：`/export/daily-duty-csv` 与 `standing-duty-csv` 在姓名匹配不到时写入固定值（`高二(2)班`、`纪检部`、`高二(1)班`），`/export/download-csv` 的"AI 归纳摘要"列实际重复了类别列；`bundle-zip` 的"下周排班表"在窗口为空时**回落到最早的 30 条历史班次**。这些导出文件不能直接作为对外公示依据。
6. **PII 暴露面（部分已收敛）**：`/students/room-members` 的手机号已对非宿管/非技术维护组脱敏，但**仍可枚举全校任意寝室**（未限制到调用者楼栋）；~~`/tech/ai-configs` 明文返回 `api_key`、`POST /welfare/gateways` 回读密钥~~ **两处均已关闭**（两个模型的 `api_key` 都是 `json:"-"` 且入库前 AES-GCM 封装，接口只给 `has_key`/`key_mask`）；仍开放的是 `/export/*` 输出手机号明文、`/publicity/broadcast/member-push` 无条件发布姓名+分值+理由。
7. **枚举不校验**：`photo_type`、`rule_type`、`action`、`change_type`、`target_department`、`period_type` 均可传任意字符串并被写库。
8. **无幂等与并发保护**：请假审批重复加分；所有福利兑换/扣额度均为"读-改-写"且无事务，可双花。
9. **审计覆盖（2026-10-01 扩充）**：已留痕的动作包括 `auth.login`、`auth.login_failed`、`auth.logout`、`auth.security_update`、`auth.dorm_quick_login`、`user.role_change`、`member.position_change`、`member.score_adjust`、`deduction.create`、`deduction.create_batch`、`deduction.revoke`、`deduction.export_csv`、`dorm.inspection.correct`、`student_roster.import`、`student_roster.overwrite_import`、`student_roster.clear_all`，以及安全中心侧的 `account.totp_setup`/`totp_enable`/`totp_disable`/`session_revoke`/`sessions_revoke_others`/`environment_confirm`、`tech.user_reset_password`/`tech.user_totp_unbind`。**凭据类动作只记"已重置/已解绑/已确认"，不记口令与 TOTP 密钥**；`deduction.export_csv` 只记条数与结构性筛选口径（楼栋/楼层/寝室/班级/类别/状态/时间），**不记学生姓名与自由检索词**；通用编辑器对 `users` 表的建/改也已留痕，明细**只列字段名不列取值**。**仍无留痕**：其余表的编辑器写操作、排班与档案类 CSV/ZIP 导出、请假审批、AI 配置修改、福利兑换与透传。
10. **AI 链路已可真实产出结论**（本地图转 base64 直传），但 `ai_status` 仍只有 `real` 才携带结论；**残留问题**：`GET /tech/db/tables/inspection_photos` 的写入接口允许手工伪造 `ai_status:"real"` 绕过 P0 约束，且 AI 端点自身不做 SSRF 校验（仅 `/welfare/*` 有）。

---

## 10. 公共响应模型（JSON 键）

| 模型 | 键 |
|---|---|
| `model.User` | `id, username, real_name, phone, role, building, floor, class_name, department, position, total_score, status, created_at, updated_at`（`password_hash` 为 `json:"-"`，不外泄）。2026-10-01 新增的 7 个安全字段 `token_version, password_changed_at, password_strength, totp_secret_enc, totp_last_step, last_login_ip, last_login_at` **同样全部 `json:"-"`** ⇒ 对外 JSON 键集不变，账户事实需经 `GET /account/security` 的 `account` 白名单视图读取 |
| `model.UserSession` | `id, user_id, login_ip, user_agent, login_at, last_seen_at, revoked_at, env_status`（`jti` 为 `json:"-"`，不外泄）。安全中心经 `sessionView` 额外给出布尔 `current`，`env_status ∈ known / pending_confirm` |
| `model.Student` | `id, student_no, real_name, grade, class_name, building, room_number, bed_number, gender, phone, status, created_at` |
| `model.DeductionRecord` | `id, student_id, building, floor, room_number, student_name, class_name, grade, category, deduct_points, reason, disposition, inspector_name, inspector_id, source_inspection_id, source_subject_id, status, revoked_by, revoked_by_name, revoke_reason, revoked_at, created_at`（`disposition` 即"如何处理"，转打表时默认取上报的 AI 处置建议；`status` 只有 `confirmed`/`revoked` 两值，**没有审核态**） |
| `model.InspectionPhoto` | `id, dorm_manager_id, manager_name, building, room_number, image_url, photo_type, report_kind, note_text, ai_status, vision_ai_output, structured_json, category, deduct_points, severity, status, review_note, created_at, processed_at` |
| `model.InspectionSubject` | `id, inspection_id, raw_name, student_id, class_name, match_status, match_note, converted_deduction_id, created_at` |
| `model.OperationLog` | `id, action, target_type, target_id, operator_id, operator_name, operator_role, detail, request_id, ip, created_at`（**只增不改**，读取走 `GET /tech/operation-logs`，仅 `tech_admin`；⚠️ `request_id` 虽在模型与 JSON 中存在，但 `controller/audit.go` 从不赋值，**落库恒为空串**，不能用于串联请求链路） |
| `model.LeaveRequest` | `id, member_id, member_name, shift_id, shift_info, reason, substitute_id, substitute_name, auto_substitute, substitute_reason, status, minister_id, minister_name, review_comment, reviewed_at, created_at` |
| `model.ScheduleShift` | `id, plan_id, date, week_type, shift_period, building, floor, member_ids_json, member_names, dorm_manager_id, manager_name, status, supervisor_pic, note, created_at` |
| `model.MemberScoreLog` | `id, member_id, member_name, shift_id, change_type, score_change, balance_after, reason, operator_name, created_at` |
| `model.AIConfig` | `id, config_key, display_name, provider, endpoint, api_key(`**`json:"-"`**`，一律不出现在响应里), model_name, system_prompt, temperature, max_tokens, is_enabled, last_tested_at, last_test_result, updated_at` |
| `model.BroadcastFeedConfig` | `id, weather_city, search_enabled, updated_at`（`search_api_key` 为 **`json:"-"`**，与 `AIConfig`/`TechWelfareGateway` 同口径：入库前 AES-GCM 封装、响应只给 `has_key`/`key_mask`）。接口不直接回吐本模型，`GET /publicity/broadcast/feed-config` 返回的是裁剪后的对象 |
| `model.BroadcastWeatherCache` | `id, date, city, province, weather, temperature, wind_direction, wind_power, humidity, fetched_at`（`date` 唯一索引 ⇒ 每天一行）。**没有对外列表接口**，只在 `POST /publicity/broadcast/ai-script` 响应的 `weather` 里以裁剪视图出现 |

---

## 11. 扩展模块（插件契约，2026-10-02 M0+M1+P1+S0+S2/S3；2026-10-03 M2+S5）

目的：后端新增一个功能模块（一个 Go 包 + `main.go` 一行路由 + 一条 Casbin 策略），Web 前端与客户端**不改代码**就多一个侧栏入口和一页内容。Win7 客户端是渲染 `static/` 的薄壳，天然跟着受益；外部 APP 团队按本节对接。

**两档共存，界面看不出差别**：编译期档（Go 包 + 重编译，§11.3）与文件期档（`plugins/` 下一个目录 = 一个独立进程插件 + 界面授权，§11.5）走**同一个**注册表、同一份清单、同一套数据端点前缀。对接方只需要认清单，不需要知道某个模块是哪一档供的。

三条硬规则（对接方必须照做，破坏任何一条都会退化成"入口点进去 403 / 白屏 / 假数据"）：
1. **未知组件 `type` 必须跳过并提示"需要更新界面版本"**，不许白屏、不许猜着渲染。老客户端活在新服务端下是常态。
2. **清单里没有的模块就是没有**。可见性由服务端按 Casbin 推导（模块内**任一**组件的数据端点读不到 ⇒ 整个模块不下发），客户端不要再按角色自己筛一遍——另存一份角色表就是第二个真相，必然漂移。
3. **取不到的数据键不渲染**。清单驱动的通用组件最容易批量产出"兜底假数据"，本项目口径是宁缺勿滥。

### 11.1 `GET /ext/modules` · JWT · 五角色
- 无参数。返回**当前身份可见**的模块清单（可见性推导规则见上；`role` 键仅为回显，不用于客户端自行过滤）。
- `200`：`{manifest_version, role, modules:[Module]}`，`Module = {id, title, icon, group, tab, min_manifest_version, widgets:[{key, type, label, data_endpoint, action_key?}], actions?:[{key, label, method, path, confirm, reason, fields[], action_endpoint}]}`
- `modules` **恒为数组**（无可见模块时为 `[]`，不会是 `null`），按 `id` 稳定排序。
- 字段约定：
  - `id` / `tab` 同一套命名规则 `^[a-z0-9][a-z0-9-]{1,39}$`（后端注册时不满足直接 panic 拒绝启动）。`tab` 会被前端拼进 DOM id 与 onclick 属性，这就是它不许带引号、尖括号或空格的原因。
  - **Web 端保留字**（与 `tab` 撞名的模块会被 Web 前端拒收并明写原因；APP 端如有自己的保留入口名，各自维护）：`dashboard / dorm / member / leave / deductions / minister / tech / export / students / welfare / publicity-gallery / broadcast-news / security / exam / excellence / messages`。
  - `min_manifest_version` 高于客户端实现版本时：**不要去请求数据端点**，显示"需要更新界面版本"即可。
  - `data_endpoint` 是**完整路径**（含 `/api/v1` 前缀），一律 `GET`；客户端不得再叠自己的 base 前缀。不带查询串、不含路径参数（`:id`）——清单是静态下发的。
  - `icon` 目前是 Font Awesome 类名（如 `fa-solid fa-server`）。
  - `type ∈ stat / note / list / table / action`（M2 起四类展示型全部可用，数据形状见 §11.2）。**认不出来的 `type` 走硬规则①**：跳过并显示"需要更新界面版本"，不猜。
  - `type:"action"` 的组件只多一个 `action_key`，指向同模块 `actions[]` 里的那一条；发起时**只 POST `{action, params}` 给 `data_endpoint`**（§11.7）。⚠️ 文件期插件的 `actions[]` 与动作组件**只含超管逐条批准过的那几条**（S5）：一个插件申请两条、批了一条，清单里就只有一个按钮、`actions` 里就只有一条——没批的那条对客户端等于不存在，别拿"清单里没有"当"接口坏了"报。
- ⚠️ 清单只描述结构，不带任何业务数据；拉清单本身不扩权。鉴权引擎未就绪时返回空清单而不是全量（失败关闭，同 `/auth/*` 口径）。

### 11.2 `GET /mod/*`（数据端点段） · JWT · 逐模块给策略
- 每个模块的数据端点挂在 `/api/v1/mod/<模块id>` 下，**不挂通用路径**：Casbin 策略按模块逐条显式书写，不依赖任何兜底通配。编译期模块目前有两条——`GET /mod/runtimestatus`（服务端运行状态）与 `GET /mod/pluginstatus`（插件加载器概览，见 §11.5），都只给 `tech_admin`：靠的是 `casbin.go` 里那条**显式写在 mod 段上**的 `role:tech_admin, /api/v1/mod/*, GET`，不是它自己的 `/api/v1/*` 兜底——授权要能在源码里逐条查到。
- ⚠️ **文件期插件的策略不在 `casbin.go` 里**：那类模块的可见性由"是否被 tech_admin 在界面上授权"决定，策略只进内存（§11.5）。在源码里查不到某个插件模块的授权，要去 `plugin_trusts` 表和 `plugin.trust` 审计记录里查——这是两档共存唯一实质差别的地方。
- `200`：`{values: {<key>: Value}}`，`Value` 是**按清单声明的类型选用的并集**：`{text, hint, level}`（`stat` / `note`）、`{items:[{label, value, hint?}], level?}`（`list`）、`{columns:[{key,label}], rows:[{<colKey>: "..."}], level?}`（`table`）。`level ∈ ok / warn`（空串按 `ok`），`hint` 是一行小字说明。**缺的键就是没有**，不会补 0 或空串。同一端点可供模块内多个组件取键。
- 形状由**服务端出口**把关（`internal/plugins/values.go` 的 `normalizeValues`，文件期插件走这一道）：控制字符剥掉、超长裁一段并汇总**一句** warn、`level` 只认 `ok`/`warn`（插件回 `"error"` 之类会按 warn 处理并写明"等级回的是 error，只认 ok / warn"）、清单里没声明的键直接丢。**形状不符整键换成一张 warn 卡，绝不部分渲染**——半屏数字看起来一切正常，是这套机制最不能接受的一种骗人。客户端仍要自己兜一层：拿不到键就按硬规则③不渲染。
- `GET /mod/runtimestatus` 的七个键（M2 起它是四类渲染器的对照样本）：`uptime` / `timezone` / `gin_mode` / `registry` 四个 `stat`（时区偏移非 `+8:00`、`gin_mode` 非 `release` 时 `warn`；`registry` 是注册模块数 / 清单契约版本 / Go 版本 / casbin_rule 条数，鉴权引擎读不到时 `warn` 并明写"读不到"）+ `provenance`(`note`，这份数据的出处) + `process`(`list`，进程事实逐行) + `registered`(`table`，注册表逐模块)。
- `GET /mod/pluginstatus` 的四个键：`loaded`（已授权且已登记进清单，不管子进程此刻活不活）、`running`（子进程当前活着，正在退避重启的那些不算）、`pending`（待授权，hint 里列出等授权的 id **并写明授权入口在技术组控制台的「AI 配置与全局数据」子标签**）、`attention`（要处理的：校验失败 / 需重新授权 / 已放弃，hint 里列出 id）。四个数全部现算，`pending>0` 与 `attention>0` 亮 `warn`。
- **授权/撤销的入口就在这一页下方**：宿主 `app.js` 在渲染 `pluginstatus` 面板时追加一块硬编码的「插件管理」表（`pluginAdminCardHtml`，见 §11.6），表里每行按状态给「授权」「改批准」「撤销」三个按钮，动作那一列是**逐条复选框 + 每条自己的批准状态**（S5）。这不是清单渲染器（`form` 类型要到 M3 才有），是宿主对这一个模块 id 的特例——其余模块仍然一行宿主代码都不加。`pending`/`attention` 两张卡的 hint 也各自指向本页下方，因为"数出有待授权"和"去哪儿点"分在两处等于让人自己找。
- ⚠️ `casbin_rule` 只增不改：模块下线时策略不会自己消失，必须跟着写显式撤销（`casbin.go` 里有先例）。**文件期插件绕开了这条**——它的策略根本不进表，所以插件下线不留残留（有测试钉住"授权+撤销一轮后 `casbin_rule` 条数不变"）。

### 11.3 加一个模块要做的事（服务端）
1. `backend/internal/modules/<name>/module.go`：描述符 + 处理器，`init()` 里 `Register(...)`。描述符不合法或 id 重复直接 panic——宁可起不来，也不上线后静默丢入口。
2. `backend/cmd/server/main.go` 一行路由注册。这行 import 同时让 `init()` 生效（Go 没有包自动发现；**不新增路由**、只把已有接口拼成清单的模块，才需要 `internal/modules/modules.go` 里一行 blank import）。
3. `casbin.go` 显式策略 `role:<角色>, /api/v1/mod/<name>, GET`，给谁写谁。策略只增不改，下线要显式撤销。
- 组件类型白名单：`stat / note / list / table / action`（前四类 M2 已实现，`action` 见 §11.7）。后端注册未支持类型会在启动时 panic——加新类型的正确顺序是**前端先有渲染器并部署，后端才开始注册用它**。清单契约版本仍是 2：老界面遇到未知类型走的已经是"需要更新界面版本"那张占位卡（它明说自己渲染不了），而 `static/` 与后端是同一个发布包出去的同一份文件，不存在"后端先上线、前端还是旧的"那种错配；真正需要抬版本的是 `action`——猜着渲染一个会改数据的按钮才是事故，那一处已经抬过了。
- **不想重编译主程序的话看 §11.5**：文件期插件走同一份清单、同一套渲染器、同一个 `/mod/<id>` 前缀，只是"注册"这一步换成"界面上授权"，Casbin 策略从 `casbin.go` 换成内存注入。两档产出的东西在客户端看来没有区别。

### 11.4 已知边界（两档通用，末两条只对文件期插件）
- 服务端目前**无法区分调用方是 Web 还是 APP**（`UserSession.UserAgent` 仅落库展示，无分流逻辑）；清单对两端同构下发。
- 无设备推送通道；`/mod/*` 只能轮询。
- Win7 客户端固定 Chromium 109：任何渲染层实现不得使用 `Object.hasOwn`(113)、`Array.prototype.at`(110)、`toSorted`(110)、CSS 嵌套(112)、`color-mix()`(111)。
- **文件期插件的两条硬限制**（要写进插件作者须知）：① 新增插件**目录**要重启主程序才被发现（授权/撤销不需要，即时生效）；② **运行中**偷换已授权文件不会被发现——指纹只在启动期与授权那一刻比对。

### 11.5 文件期·独立进程插件（P1，2026-10-02）

一句话：**插件 = 服务器上 `plugins/` 下的一个目录，丢进去不等于上线**。技术维护组在侧栏「插件加载器」页（`pluginstatus`）下方的「插件管理」表里输口令授权后它才运行、才进清单；授权绑定的是那一刻的文件指纹，换了文件就要重新授权。方案与理由见仓库根《学管会系统_独立进程插件方案.md》（§16 是该轮的落地记录）；**要动手写插件的人看《学管会系统_插件开发指南.md》**（本节讲"接口承诺了什么"，那份讲"怎么写才不踩坑"：可跑的骨架、逐字段规则、报错原话对照表、提交前自查清单）。

**目录规范**

```
plugins/
  diskusage/            ← 一个子目录 = 一个插件；根目录下的散文件不算插件
    manifest.json
    plugin              ← Linux；Windows 上是 plugin.exe
    signature           ← 开发者签名，一行 JSON（S0 起必需，见下）
```

**上线要过两道门：先有可信开发者的 `signature`（没有它连"待授权"都排不上），再由 tech_admin 在界面上授权。** 签名对象是 `sha256(manifest.json) ‖ sha256(可执行文件)`，用 Ed25519（Go 标准库，CGO=0 可编）；`signature` 文件形如 `{"kid":"…","manifest":"…","exec":"…","sig":"base64"}`——两份摘要也写进去，是为了让 `pluginctl verify` 能指出"是哪一份变了"，而摘要本身被签名覆盖，改摘要等于伪造签名。公钥来源：主程序内置锚 + 运行目录 `plugin_keys/*.pub`（**与 `plugins/` 同级，不放里面**）；界面上加公钥这条路明确不做。作者侧的操作与工具见《学管会系统_插件开发指南.md》§2.2。

`manifest.json` 字段（**解码拒绝任何未知字段**：`policy` 手滑写成 `policies` 会被判"清单不合法"并列在面板上，而不是静默变成"谁都不给看"）：

| 字段 | 必填 | 规则 |
|---|---|---|
| `id` | 是 | `^[a-z0-9][a-z0-9-]{1,39}$`；与编译期模块或别的目录撞 id ⇒ 校验失败，列在面板 |
| `title` | 是 | 空 = 校验失败（"侧栏会是一个认不出来的图标"） |
| `icon` / `group` | 否 / 否 | **服务端不校验 `icon`**，不填由前端兜底成 `fa-solid fa-cube`；`group` 决定侧栏分组，不填进"扩展模块"组 |
| `tab` | 是 | 同 `id` 规则；与内置面板名撞由**前端**拒收并明写原因（服务端不复制那份保留字名单） |
| `policy` | 否 | 角色数组，只认 `dorm_manager`/`member`/`minister`/`tech_admin`/`viewer_export`；出现未知角色 ⇒ 校验失败；**缺省 = 仅 `tech_admin`**（"忘了写"不许等于"全校都能读"）。会去重并按这五个角色的固定次序排好 |
| `widgets` | 是 | `[{key, type, label}]`；`type` 必须在白名单（目前只有 `stat`） |
| `exec` | 否 | 只能是**一个裸文件名**，带路径分隔符即校验失败（防止把加载器引到插件目录外）；缺省 Windows=`plugin.exe`、其它=`plugin` |
| `min_manifest_version` | 否 | 不填由服务端补当前契约版本；手填且 ≠ 当前版本 ⇒ 校验失败（"这插件不是照着现在的接口写的"） |

- `data_endpoint` **由 id 推导**（`/api/v1/mod/<id>`），manifest 里写不了别的路径：一个插件只该有这一个入口，能自己填路径就等于允许它声明"我这个组件的数据其实在那个业务接口上"，那已经不是插件，是绕过 Casbin 的第二套路由表。
- 可执行文件四条要求：读 stdin 一行、写 stdout 一行（UTF-8）；不做网络监听；崩溃无所谓（由主程序拉起）；日志写 stderr。

**stdio 协议（行式 JSON，协议版本 1）**

```
→ {"id":1,"op":"ping"}
← {"id":1,"ok":true,"protocol":1}

→ {"id":2,"op":"data","keys":["data_disk"],"user_id":3,"role":"tech_admin"}
← {"id":2,"ok":true,"values":{"data_disk":{"text":"79% 已用","hint":"共 731.5 GB，余 150.1 GB","level":"ok"}}}
← {"id":2,"ok":false,"error":"读不到传感器"}     ← 插件自报错误，会截断转义后进警示卡
```

- **`id` 必须原样回显**，对不上判串号并 kill；响应行 ≤ **256KB**；单请求 **5s**；同一插件的请求**串行**。
- 子进程环境变量：`XGH_PLUGIN_ID`，Windows 上另发 `SystemRoot`/`WINDIR`（Go 运行时在 Windows 上要靠它定位系统目录，这是实现中对方案字面口径的一处偏离，已在代码注释里写明）。**不传 `DB_PATH`、不传任何密钥。** stdin 关闭（EOF）时插件应自行退出。
- `values` 的形态与 §11.2 完全一致：**插件没返回的键就是不存在的键**，主程序不补 0、不补空串，前端显示"无数据"。

**信任门与状态**（`View.state`，管理面板「状态」列的八个取值）

| `state` | 含义 | 下一步 |
|---|---|---|
| `pending` | 待授权：**签名已经过了**、校验过了，没人授权 ⇒ 不运行、不进清单 | 界面授权 |
| `running` | 已授权且子进程活着 | — |
| `retrying` | 崩溃/超时后正在退避重启（1s→…→60s 封顶） | 看一眼 `last_error`/`stderr_tail` |
| `gave_up` | 连续失败 10 次，不再自动拉起 | 重新授权可再拉一次 |
| `invalid` | manifest 校验失败，`reason` 给原因 | 改文件；此类目录在列表里按 `#目录名` 显示（没有可用的 id） |
| `stale` | 文件与授权时指纹不符 ⇒ **不拉起、不注册、不注入策略**（作者签了另一份，人批的是先前那份） | 重新授权 |
| `unsigned` | 没有 `signature` 文件，或签名者不在信任锚里 | **找作者要签名 / 装公钥**，不是改 JSON；`signed_by` 有值时它就是"声称谁签的" |
| `sig_broken` | 有签名但磁盘上的文件与它不一致，或签名本身验不过 | 可能是篡改：`pluginctl verify` 定位是哪一份变了。界面用**红**而不是琥珀 |

**坏 manifest 不拒绝启动**（这是对早期"拒启"口径的正式修正）：信任门已经把一切丢入物挡在运行之外，一个手滑的 JSON 逗号不该放倒全校系统。

**一条结构性事实，对接方必须知道**：**gin 不能在运行期加路由**。所以 `/mod/<id>` 是在**启动期**为每一个**校验通过**的插件（不论是否已授权）挂好的。"未授权"不体现为 404，而体现为：① 它不在任何人的清单里（注册表没有 ⇒ `/ext/modules` 不下发）；② 硬打那条路径时，有 Casbin 权限的 `tech_admin` 拿到 200 + warn 卡（明写"插件未授权，只在管理面板里列着，不运行"），其余角色 403。**入口不存在 ≠ 路径不存在**——判断一个插件能不能用只看清单，不要拿"试打路径"当探测手段。

**失败语义**：无进程 / 已放弃 / 传输失败（超时、坏 JSON、串号）/ 插件自报 `ok:false` ⇒ **一律 HTTP 200**，请求的每个键都是 `{text:"读取失败", hint:<原因>, level:"warn"}`。原因按情况分句写清（未授权 / 校验失败+原因 / 退避重启中 / 已放弃 / `plugins/` 下没有这个目录），因为这几种的下一步动作完全不同，合成一句"未运行"等于让运维自己猜。

**上线一个新插件要走五步**：插件作者**签名**（`pluginctl sign`，私钥只在作者手上）→ 交目录（含 `signature` 文件）→ 技术维护组放进 `plugins/` → **重启主程序**（新目录只有重启才会被发现）→ 界面授权（授权/撤销本身免重启、即时生效）。**签名和授权是两道独立的门**：签名证明"这份东西确实是某个已登记的开发者发的"，由服务端在每次扫描时验；授权证明"这台服务愿意让它跑"，由技术维护组点。只有签名者公钥已装进 `plugin_keys/` 的插件才可能进入待授权状态——没装公钥的开发者，他的插件一律停在 `unsigned`，摸不到授权按钮。上限：插件数 ≤ 8、响应行 ≤ 256KB、单请求 5s，写死在代码里而不是配置项（留一个"最大插件数"的环境变量，出问题当晚就会有人调大）。

### 11.6 `GET /tech/plugins` + 授权/撤销 · JWT · 仅 tech_admin

三条路由挂在既有 `/api/v1/tech` 段下——那段本来就全方法仅 `tech_admin`，**不需要为插件另写 Casbin 策略**，"谁能授权插件"因此只有一个答案。⚠️ 但**界面入口不在技术组控制台**：Web 端把这张表渲染在侧栏「插件加载器」（`pluginstatus`）那一页下方。鉴权段和界面位置是两件事，对接方按路径鉴权、按 §11.2 那一页找入口。

- **`GET /tech/plugins`** → `200`：`{plugins_dir, trusted_keys:[kid], max_plugins, plugins:[View], stats:{loaded, running, pending, attention}}`
  - `plugins_dir` 是加载器扫的那个目录（`PLUGINS_DIR` 可覆盖，默认 `./plugins`）；`stats` 与 §11.2 的 `pluginstatus` 四键同源。
  - `trusted_keys` 是**这台服务当前认的所有开发者 kid**（公钥目录 `plugin_keys/` 里的 `.pub` 文件名去后缀 + 内置锚）。界面靠它说一句"可信开发者 N 位"；**目录为空时列表就是 `[]`**，此时任何插件都签不过、全部停在 `unsigned`——这是"失败关闭"，不是 bug。前端会在 `trusted_keys` 为空时直接显示"这台服务还没配可信开发者公钥"。
  - `View` 一行 = 一个插件：`id`（校验失败时为 `#目录名`）、`dir`（**目录名，不是全路径**）、`title`、`state`、`reason?`、`policy_roles`、`widgets`（组件 key 的字符串数组）、`exec_hash` / `manifest_hash`（**当前**磁盘上那份的 sha256，64 位全串）、`trusted_exec_hash?` / `trusted_manifest_hash?`（**授权那一刻**记下的指纹；界面要把"当初授权的是哪份"和"现在是哪份"并排给人看，只回当前指纹的话 `stale` 那行没人看得出为什么）、`trusted_at?` / `trusted_by?`（`trusted_by` 是**姓名**不是 id）、`revoked_at?` / `revoked_by?`、`last_error?`、`stderr_tail?`（每个插件只留最后 4KB）、`manifest_raw?`（磁盘上的原始字节，不是重新序列化过的）、`signed_by?`（**签名文件里声称的开发者 kid**，16 位 hex；`unsigned`/`sig_broken` 时它是"这人自称是谁"，正常时配合 `trusted_keys` 就能看出认不认得这个人）、`actions?`（**S2/S3 起**：这个插件**申请过**的全部写动作——完整申请单，含没批准的，逐条 `{key, label, method, path, confirm, reason, fields[], action_endpoint, granted, granted_by?, granted_at?}`）。⚠️ 批准状态挂在每条自己的 `granted` 上，**不在插件级**：一个申请三条、批了两条的插件，`actions` 仍是三条，其中两条 `granted:true`。而它下发给客户端的清单（§11.1）里只有被批准的那几条会长成按钮。⚠️ `actions` 出现在**授权之前**的那一行里是刻意的：人在点"授权"之前必须看得到"它会改哪条接口、要不要口令、它自己说是为什么"，否则授权退化成点按钮。`confirm` 这一列**已经是服务端高危名单改写后的值**（插件自报 `none` 不算），界面据此显示"执行前要输口令"。
  - 只回元数据与状态：**这里没有任何密钥**（本来也没有），也不回 manifest 之外的任何文件内容。
- **`POST /tech/plugins/:id/trust`**、**`POST /tech/plugins/:id/grants`** 与 **`POST /tech/plugins/:id/revoke`**：三条都**必须带 `X-Confirm-Password`**（登录口令二次确认，走请求头而不是请求体，免得和 JSON 绑定抢同一个 body）。撤销也要口令——两把钥匙的口径对称，才不会留下"开一扇门要密码、关一扇门不用"这种能被会话劫持反着利用的缝。
  - **`trust` 与 `grants` 收请求体 `{"actions":["<清单里的动作 key>", …]}`**（S5）：`trust` 是"上线它 + 批准这几条写动作"一次做完，`grants` 只改批准范围、不动授权与进程。⚠️ 只认 `actions` 这一个键，多塞 `path`/`method` 会被 `400` 当未知字段拒掉——**批的到底是哪条接口由服务端手里那份签名清单决定**，客户端能说的只有 key；空 body 合法（= 一条都不批，只放行读数），服务端**不替任何人默认全批**。界面上勾完必须点「改批准」才生效，这张表没有自动保存。
  - **批准记录绑文件指纹**（`plugin_action_grants` 表，一行一条）：重新授权时旧行全部标撤、新行按这一次勾的重写；换过文件之后旧批准**不随文件走**（启动期按 `exec_hash` 比对，不符就不挂回，并在启动日志里点名是哪几条）。撤销整插件时逐条标撤、**不删行**——删了就没人能回答"这个插件当时被批准过什么"。
  - 响应：成功 `200 {plugin: View}`；插件不存在或状态不允许授权/撤销 `400 {error}`；step-up 侧还有 **403 `code:"password_change_required"`**（仍在用初始口令的账号先改密）、**400**（没带确认头，不计入失败次数）、**401**（口令错，计入失败次数）、**429**（连续失败过多，带 `retry_after` 秒数；限流键 `stepup|账号|IP`，与登录共用那套 5 次 / 15 分钟）。
  - **授权对签名是"零信任"的**：`unsigned` 和 `sig_broken` 两种状态会被 `400` 拒掉并写明原因（"没有可信开发者签名" / "签名与磁盘上的文件对不上"）。界面把按钮藏起来只是省人一次点击，**门在 `Authorize` 里面**：绕过界面直接 POST 也一样被拒，只挡前端的门等于没门。⚠️ 反过来说，**装了某人的公钥不等于信任他的插件**——公钥只让他从 `unsigned` 走到 `pending`，那一步之后仍然要有人点授权。
  - **授权这一刻会重读磁盘**（不只是看上次扫描的缓存）：`Authorize` 先把这个目录重新扫一遍（重算两份摘要 + 重验签名），再决定放行不放行。原因是面板上那行"待授权"是上一次扫描（通常就是主程序启动）时的快照，从"人看见它"到"人点下授权"之间文件可以被换；按缓存授权的话，绑定的是旧指纹、起来的却是新那份，签名与指纹两道门同时失效。三种结果：① 重扫后签名不过 ⇒ `400`（原因即上面那句）；② 签名过但**内容与列表里那份不同** ⇒ `400`「文件在上次扫描之后被换过，本次授权没有生效：列表已刷成现在这份，请核对标题、可读角色与开发者后再点一次」——**刻意不"默默改绑新内容"**，人批准的是他在页面上读到的那份；拒绝之前服务端已把缓存刷成磁盘现状，所以界面重读后那一行显示的是真话，第二下点的就是新那份；③ 一致 ⇒ 照常授权。
  - 授权的执行顺序是**先验口令、再动真格**（口令没过的请求不该让任何进程被拉起来），然后 spawn → ping（10s）→ 进注册表 → 注入策略 → 写信任记录，**任一步失败就把它之前做过的逐步回滚**（起不来就报"插件起来了但不回话"并 kill；写库失败会把策略和注册表都撤回去）；留痕**写在成功之后**——"这个入口被放开了"这句话必须和事实一致。
  - 重复授权会 `400`「已在授权状态，无需重复授权」，**唯一例外是 `stale`**：换了文件的那一行允许（也必须）重新授权一次，授权成功即把状态从 `stale` 收回。
  - 撤销的顺序与授权相反，且**每步失败都继续往下走完**：最坏情况是库里那行没标成撤销（下次重启会被重新拉起），但绝不能出现"点了撤销、这一刻它还在给人读数据"。撤销一个 `stale` 插件是干净成功（它本来就没在跑），不算状态错配。
  - 审计动作：`plugin.trust` / `plugin.grants` / `plugin.revoke`（`target_type="plugin"`，经 `GET /tech/operation-logs` 读）。detail 写的是 id、目录、**exec sha256 与 manifest sha256 全串**、**开发者 kid**（`开发者 36f56d2f…`，即"放开的这个入口是谁写的东西"）、可读角色清单、执行人；**不含口令**。从这一刻起"这个入口被放开了"和"里面那份代码出自谁的手"都是数据库记录，不再取决于谁碰过服务器文件系统。
- 策略只进内存：`Enforcer.EnableAutoSave(false)` + `AddPolicy`/`RemovePolicy("role:<角色>", "/api/v1/mod/<id>", "GET")`。**`casbin_rule` 表仍然只归 seed 管**，插件授权、撤销、下线都不落表、不留残留（测试钉住"授权+撤销一轮后表内条数不变"）。声明过动作的插件在这里是**两条**策略：`GET /api/v1/mod/<id>`（读数）与 `POST /api/v1/mod/<id>/action`（发起动作）。两条一起注入、一起撤销，**半套失败就整套回滚**——留下半套的意思是"能点按钮但读不到数"或反过来，两种都比全没放开更难查。

### 11.7 `POST /mod/<id>/action` · 插件写动作（S2/S3，2026-10-02 夜）

**这一条路由只在"这个插件声明过动作"时存在**（启动期决定，gin 不能运行期加路由）。只读插件（`diskusage`）压根挂不上它；被撤销的插件路由仍在，打它得到的是明写的"未授权"，不是 404。

- 请求体：`{"action":"<清单里的动作 key>","params":{"<字段>":"<文本>"}}`。**`method`/`path` 不接受**——多塞一个键会被 `400` 当未知字段拒掉（客户端替清单做主的表现是"我明明指了那个接口它却没动"，那句原因会被掩盖，所以宁可当场拒）。
- 响应 = **代执行那一跳的原样转发**：业务接口回 201 就回 201、回 403 就回 403，`Content-Type` 与 body 都原样带出。**不改写成 200**：把 403 说成成功，界面上会显示"已完成"而库里什么都没变。
- 状态码含义（对接方按码分支，别按中文文案匹配）：

| 码 | 这一句话是什么 |
|---|---|
| `400` | 请求体读不懂 / 缺 `action` / 清单里没声明过这个 key / 参数不合清单（未知栏、缺栏、超长、非文本） |
| `401` | 会话失效；或高危动作的二次确认口令错（计入 `stepup\|账号\|IP` 限流） |
| `403` | **内层业务接口自己拒的**——那个人的角色对那条接口没权。这是"插件不可能越操作者的权"的正面证据，body 就是 Casbin 那句原话 |
| `409` | 插件状态不允许（未授权 / manifest 校验失败 / 没有可信签名 / 签名与文件对不上 / 文件在授权后被换过），**或这条动作没被超管批准**（S5：「插件 X 已授权，但这条动作 "key" 没被批准：在「插件管理」那一行勾上它（或改批准范围），动作才会执行」）。**两道门都排在要口令之前**，所以未授权、未批准的插件都不会先让你输一遍登录口令才告诉你它不行 |
| `429` | 同一个人 60 秒内已经发起过一次同样的动作（参数摘要相同）。窗口只在服务端进程内存里，重启即清 |
| `502` | 插件越界（它回的 `method`/`path` 与签名清单不符）/ 插件进程没在跑 / 插件回了 `ok` 却没回 `request` / 那一跳走不通。**最后一句不断言成败**："走不通"不等于"没写进去"，先去看对应页面再决定要不要点第二下 |
| `2xx` | 业务接口自己说成了（`created_by` 那一列记的是**点按钮那个人**，不是插件） |

- **能声明哪些接口由服务端决定**，`internal/plugins/action_scope.go` 两份清单：`declarableActions`（可声明白名单，首批只放"新增一条且事后撤得回"的两条）与 `forcedStepUp`（高危名单，命中即强制 `stepup`，与插件自报的 `confirm` 无关）。三段前缀点名拒绝：`/api/v1/tech/db/*`、`/api/v1/mod/*`、`/api/v1/auth/*`。**不在白名单的动作，manifest 写了就在扫描期被判 `invalid`**，进不了可授权列表——所以"对接方能不能让插件多改一个接口"这个问题，答案不在本文档里，在那个文件和一次评审里。
- 高危动作需要 **`X-Confirm-Password`**（与 §11.6 那两条同源，走请求头）。⚠️ 口令**一行都不进那根 stdio 管道**：它是宿主自己用来打本机那一跳的，插件既看不到也不该看到。
- 内部标记头 `X-Plugin-Acted-For: plugin=<id> action=<key>` 由宿主自己拼，**非回环来源带上这个头会被中间件先剥掉**。它给业务处理器的是"这一趟是插件代出来的"这条上下文，不是权限，也不是可信凭据。
- 审计：**每一次尝试都落一条 `plugin.action`**（含被 409/403/502 挡下的那些），detail = 插件 id、动作 key 与 label、`宿主代执行 METHOD path`、参数**名**（不含值）、结果（`HTTP <码>` 或"未执行/未送达"+原因）、是否已二次确认；IP 取**外层请求**的真实来源。读法见 §11.6 最后一条与《方案》§14 第 10 条：外层那条审计有真实 IP，内层业务处理器自己那一份只会是 `127.0.0.1`。**被批准门（下面的 409「没被批准」）挡下的那一次也留一条**，结果写「未批准」——"有人试图发起一条没被批准的写动作"正是这条门最需要留下痕迹的那种尝试。
- **门序（S5 之后写路径上有四道，顺序是定的）**：`DeclaredAction`（清单里没这个 key → 400）→ `ActionGate`（插件状态不允许 → 409）→ `GrantGate`（这条动作没被批准 → 409）→ 高危才要 `X-Confirm-Password`。**三道门全部排在要口令之前**：一个没授权、或没批这条动作的插件，不该让人先输一遍登录口令才听到那句"不行"。
- 清单侧的字段规则与作者口径在《学管会系统_插件开发指南.md》§2.3；这一节的三条硬约束（不采信客户端的 method/path、忽略插件塞的头、越界即拒）都有单测与端到端记录（《方案》§18）。

---

## 附：本文未覆盖

- 前端各页面实际调用了哪些接口（部分接口**有路由无界面**：`/export/exam-submissions`、`/tech/ai-configs`、`/tech/overview`、`POST|PUT /publicity/broadcast/news*`、`/welfare/gateways/:id/exchange`）。2026-10-01 起 `POST /publicity/broadcast/ai-script` 与 `GET|PUT /publicity/broadcast/feed-config` **已有界面**（播音组面板、技术组控制台）。
- ⚠️ 另有两条**既无界面也无本文条目**的别名路由：`GET /publicity/broadcast-news`（等于 `/broadcast/news`）、`GET /publicity/broadcast-rank-push`（等于 `/broadcast/member-push`）。行为与正本一致，但按路径鉴权或做统计时会被重复计入，需要的话应显式删掉一条。
- `operation_logs` 的读取入口是 `GET /tech/operation-logs`（仅 `tech_admin`）：支持 `action`（精确匹配）与 `operator`（操作者姓名 `LIKE` 模糊）两个过滤参数，`page`/`page_size`(≤200)，按 `id desc` 返回。
- 未做真实浏览器/客户端联调，所有响应形态来自源码核对与接口实测。
- **站内信 5 条路由已上线（2026-10-02，A1–A3）但尚未写成正式条目**：`GET /messages`（收件箱+发件箱，`box=inbox|sent`）、`POST /messages`（选人发送）、`GET /messages/unread-count`、`GET /messages/contacts`（选人列表）、`PUT /messages/:id/read`（清未读，幂等）。策略口径见 §2.2 表内注（§附）：查看岗只收不发。补写正式条目前以 `internal/controller/message_controller.go` 为准。
