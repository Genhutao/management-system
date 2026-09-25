# 学管会宿管端 App（原生 Android，Kotlin）

`dorm_manager` 宿管专属原生客户端，第一版功能：

- **三要素免密登录**：手机号 + 楼栋 + 姓名 → `POST /auth/dorm-quick-login`，签发 JWT 后持久化，打开 App 自动直登（`GET /auth/profile` 校验）
- **今日待办**：`GET /dorm/today-tasks` 待办卡片 + `GET /dorm/slot-notice` 时段剩余分钟/已上报数，下拉刷新
- **现场上报**：三态（实拍/记名纸条/纯文本）→ `POST /dorm/upload-photo`（multipart `image` 字段）；展示 `ai_status` 徽标与名单命中数
- **历史上报**：`GET /dorm/inspections` 两列瀑布流 + severity 过滤
- **设置**：服务器地址 App 内可配置（持久化）、退出登录

## 技术栈
Kotlin + XML View 布局、ViewBinding、Retrofit + OkHttp、Coil、DataStore、TakePicture + FileProvider（无需相机权限）。

## 导入与打包
1. Android Studio（建议 Hedgehog+，Gradle 8.7 / AGP 8.5 / Kotlin 1.9）→ Open 选择 `mobile-apk/xgh-app`
2. 等 Gradle Sync 完成（依赖源可按需配置国内代理）
3. `Build > Build Bundle(s)/APK(s) > Build APK(s)` 生成 `app/build/outputs/apk/debug/app-debug.apk`

## 联调
1. 手机与后端同一内网；登录页"服务器地址"填 `http://<后端IP>:8080`
2. cleartext HTTP 已在 `network_security_config.xml` 全局放行（校园内网 HTTP 部署）
3. 楼栋请输完整名称（如 `12号楼`）——后端三要素是双向子串匹配，只输数字会匹配错楼栋

## 已知后端约束（对接时已按文档处理）
- `report_kind != "text"` 时无图直接 400，App 端提交前已校验
- `ai_status` 目前只会是 `disabled/failed/unknown`，App 显示"需人工核对"，不展示任何编造结论
- 上报/历史接口按 JWT `building` 隔离；图片返回 `/uploads/...` 相对路径，App 端拼 base URL
