# 学管会宿管端 App（原生 Android，Kotlin）

`dorm_manager` 宿管专属原生客户端。

## 功能

- **三要素免密登录**：手机号 + 楼栋 + 姓名 → `POST /auth/dorm-quick-login`，签发 JWT 后持久化，打开 App 自动直登（`GET /auth/profile` 校验）
- **今日待办**：`GET /dorm/today-tasks` 待办卡片 + `GET /dorm/slot-notice` 时段剩余分钟/已上报数，下拉刷新；从上报页返回会自动重拉最新计数
- **现场上报**：三态（实拍/记名纸条/纯文本）→ `POST /dorm/upload-photo`（multipart `image` 字段）；上报类别固定三选一（violation / sanitation / duty），与后端时段计数统计口径一致——时段计数按 `photo_type` 精确匹配，请勿绕过选择器手动传其他值；展示 `ai_status` 徽标与名单命中数
- **历史上报**：`GET /dorm/inspections` 两列瀑布流 + severity 过滤
- **设置**：
  - 服务器地址 App 内可配置（持久化），默认 `http://103.236.77.86:19198`
  - 字号大小：标准 / 大（1.15x）/ 特大（1.3x），适老化，全局即时生效
  - 深色模式：跟随系统 / 浅色 / 深色
  - 退出登录
- **界面**：Material 3 组件与矢量图标，明暗双主题，深色模式下顶栏与状态栏同色

## 技术栈

Kotlin + XML View 布局、ViewBinding、Material Components 1.12（M3 主题）、Retrofit + OkHttp、Coil、DataStore（会话与偏好分库：`xgh_session` / `xgh_prefs`）、TakePicture + FileProvider（无需相机权限）。

自绘 Material 风格矢量图标位于 `app/src/main/res/drawable/ic_*.xml`，填充色为 `?attr/colorOnSurface`，随主题自动适配明暗。

> 注：主题里的 `colorInverseSurface` 等 token 在 material 1.12.0 中未声明为 attr（属于 Compose M3 命名），由 `values/attrs.xml` 在应用侧补齐。

## 导入与打包

1. Android Studio（建议 Hedgehog+，Gradle 8.7 / AGP 8.5 / Kotlin 1.9）→ Open 选择 `mobile-apk/xgh-app`
2. 等 Gradle Sync 完成（依赖源可按需配置国内代理）
3. `Build > Build Bundle(s)/APK(s) > Build APK(s)` 生成 `app/build/outputs/apk/debug/app-debug.apk`

### 命令行打包

- 需要 **JDK 17+**（系统仅 JRE 8 时 AGP 会直接报错），本机已装在 `C:\Users\yuyanfeng\jdks\jdk-17.0.20.1+1`
- `local.properties` 指向 SDK（已配置，指向 `%LOCALAPPDATA%\Android\Sdk`，不入库）
- wrapper 的 `distributionUrl` 指向腾讯镜像（本机网络访问 services.gradle.org 不通）

```bash
cd mobile-apk/xgh-app
export JAVA_HOME="$USERPROFILE/jdks/jdk-17.0.20.1+1"
./gradlew.bat assembleDebug
# 产物：app/build/outputs/apk/debug/app-debug.apk
```

安装到模拟器/真机：`adb install -r app/build/outputs/apk/debug/app-debug.apk`

## 联调

1. 默认服务器 `http://103.236.77.86:19198`（登录页可改）；手机/模拟器与后端网络互通即可，模拟器访问本机服务用 `http://10.0.2.2:<端口>`
2. cleartext HTTP 已在 `network_security_config.xml` 全局放行（校园内网 HTTP 部署）
3. 楼栋请输完整名称（如 `12号楼`）——后端三要素是双向子串匹配，只输数字会匹配错楼栋
4. "本时段已上报 N 条"按当前时段配置的 `photo_type` 精确计数，上报时类别要选对

## 已知后端约束（对接时已按文档处理）

- `report_kind != "text"` 时无图直接 400，App 端提交前已校验
- `ai_status` 目前只会是 `disabled/failed/unknown`，App 显示"需人工核对"，不展示任何编造结论
- 上报/历史接口按 JWT `building` 隔离；图片返回 `/uploads/...` 相对路径，App 端拼 base URL
- 后端时段计数（`slot-notice`）按 `building LIKE` + `photo_type =` 精确过滤，跨类别/楼栋名不一致时计数不涨属预期
