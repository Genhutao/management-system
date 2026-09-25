# 学管会移动端 Android APK 封装与手机号登录机制说明

本系统采用 **现代化自适应响应式 Web + 原生 Android APK 混合架构**，网站端与手机 APK 共享同一套 Go 后端高并发服务与 Casbin 权限引擎。

---

## 一、 登录鉴权差异化设计

根据您的业务需求，系统实现了差异化的终端登录策略：

| 终端类型 | 账号密码登录 | 宿管手机认证直接登录 | 说明 |
| :--- | :--- | :--- | :--- |
| **Web 网站端** |  支持 (五大角色通用) | 仅作为预置花名册查看 | 适合 PC/桌面操作、大屏监控与报表导出 |
| **Android APK 手机端** |  支持 (备用通道) |  **专享一键直登** | 首次输入【手机号+楼栋+姓名】三要素自动比对预置花名册并完成设备绑定，后续免密直登 |

### 手机号检测与三要素认证逻辑
1. **系统底层限制**：现代 Android (10/11/12/13/14+) 系统出于隐私政策，普通 App 无法直接通过系统 API 静默明文抓取 SIM 手机号。
2. **极简三要素认证方案**：
   - 技术维护组在后台提前录入或导入《宿管花名册》（包含：真实姓名、手机号码、负责楼栋楼层）。
   - 宿管在手机 APK 端首次打开时，直接输入自己的手机号、姓名和楼栋。
   - 后端核验成功后，自动为该宿管激活并签发持久 JWT Token，存储于本地 SQLite / SharedPreferences 中。
   - 宿管以后每次打开 APK 无需再输任何信息，直接进入**大字版宿管极简工作台**！

---

## 二、 移动端 APK 快速构建与打包方案

您可以选择以下两种主流开源方式将系统打包为 `.apk` 安装包：

### 方案 A：通过 Capacitor 一键打包（推荐，极简跨平台）
在 `management-system` 目录下执行：
```bash
# 1. 安装跨平台移动端引擎
npm install @capacitor/core @capacitor/cli @capacitor/android

# 2. 初始化移动端工程并添加 Android 平台
npx cap init "学管会管理系统" "com.xgh.management" --web-dir "backend/static"
npx cap add android

# 3. 同步前端代码并打开 Android Studio 构建 APK
npx cap sync
npx cap open android
# 在 Android Studio 中点击 Build -> Build Bundle(s) / APK(s) -> Build APK(s) 即可生成正式 release.apk！
```

### 方案 B：标准 Android 原生 WebView 工程
1. 打开 Android Studio，新建一个 Empty Activity 工程。
2. 将 `mobile-apk/AndroidManifest.xml` 替换至 `app/src/main/AndroidManifest.xml`。
3. 在 `MainActivity.java` 或 `MainActivity.kt` 中载入服务端地址：
   ```kotlin
   val webView = findViewById<WebView>(R.id.webView)
   webView.settings.javaScriptEnabled = true
   webView.settings.domStorageEnabled = true
   // 允许拉起手机系统相机
   webView.webChromeClient = object : WebChromeClient() { ... }
   // 载入学管会系统后端地址（例如学校内网服务器 IP 或公网域名）
   webView.loadUrl("http://您的服务器IP:8080/")
   ```
4. 点击 Build 即可输出 APK。

---

## 三、 APK 专属特色体验
- **相机硬件直接调用**：拍照按钮直接调起手机后置摄像头，无需二次确认。
- **工作时间自动推送**：19:00~21:00 等查寝时段，APK 首页全屏呈现待办提醒：“今日监督打卡：请对当值部员拍照”、“宿舍查寝：拍照上传”。
- **大字版与高对比度**：专为宿管阿姨与巡查部员优化触控体验。
