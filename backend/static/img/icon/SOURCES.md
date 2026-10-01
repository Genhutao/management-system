# 站点图标出处

与 `static/img/wallpapers/SOURCES.md`、`static/vendor/SOURCES.md` 同一惯例：本地化后同源加载，登记原始文件、取回日期、许可证与 sha256，便于授权回溯与复现。

## 来源

徽记原图由管理员提供，**不是网络素材**：

| 项 | 值 |
|---|---|
| 原始文件 | `docs/微信图片_20260926191542_1131_101.png`（664×664，RGB 无透明通道） |
| 原始 sha256（前 16） | `dde853d0685e042b` |
| 许可 | 管理员自有素材，随仓库内部使用；未做二次创作，仅等比缩放 |
| 生成日期 | 2026-10-01 |
| 生成方式 | `backend/.verify/gen_icons.py`（Pillow 12.3.0，LANCZOS 缩放）。该脚本在 `.verify/` 下，不随发布包分发；重跑可逐字复现下列全部产物。 |

## 文件对照

| 文件 | 尺寸 | 用途 | sha256（前 16） |
|---|---|---|---|
| `favicon.ico` | 16/24/32/48/64 五条目 | 浏览器标签页与地址栏兜底（IE/旧 Edge 及部分浏览器仍优先取 ico） | `d40b7a3ca0c1d4bc` |
| `xgh-icon-192.png` | 192×192 | `<link rel="icon" sizes="192x192">`，桌面与安卓浏览器新建标签 | `931d70983e331e56` |
| `xgh-icon-512.png` | 512×512 | `<link rel="icon" sizes="512x512">`，高分屏与"添加到主屏" | `cc1832365ef74bb1` |
| `apple-touch-icon.png` | 180×180 | iOS Safari 添加到主屏 | `a24531a944d1d720` |

引用位置：`backend/static/index.html` 的 `<head>`（`rel="icon"` ×3、`rel="apple-touch-icon"`、`theme-color=#f4f5f7`）。

## 已知取舍

- **16/24px 可读性差**：原图是近白底 + 细轨道线 + 小星点，缩到 16px 后细节基本消失，在深色浏览器主题下像一个浅色圆块。本轮按"忠实呈现原图"处理，未做描边、未加底板、未改配色。若要小尺寸可用，需要单独出一版"16px 优化稿"（加深对比或加圆形底板），属再创作，需另行确认。
- 原图无透明通道，产物同样无透明通道；`favicon.ico` 内为不透明位图。
- 未做 PWA `manifest.webmanifest`（本系统不离线安装，`theme-color` 已足够）。
- 桌面客户端（`desktop-client/`，未被 git 跟踪）与安卓端（图标在 `android-app` 分支的 `mipmap`）本轮**未替换**。
