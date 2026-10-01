# 第三方静态资源出处

本目录内的第三方文件一律本地化后同源加载（校园网/内网隔离时不得依赖公网 CDN）。新增资源必须在此登记：原始 URL、取回日期、sha256、许可证。

## qrcode-generator-1.4.4.js

| 项 | 值 |
|---|---|
| 用途 | 账户安全中心「绑定动态口令」时，把服务端返回的 `otpauth://` 配置链接画成二维码，供验证器 App 扫码 |
| 上游 | Kazuhiko Arase, `qrcode-generator` v1.4.4 —— `https://cdn.jsdelivr.net/npm/qrcode-generator@1.4.4/qrcode.js` |
| 许可证 | MIT（文件头保留原作者版权声明与 MIT 条款原文，未改动） |
| 取回日期 | 2026-10-01 |
| 大小 / sha256 | 56,694 字节 / `18ae399f81182bc9de916e9c77b195df20cc58d6f2d55a62b085a299f1bf1780` |
| 加载方式 | `<script src>` 经典脚本。文件末尾的 UMD 包装只处理 AMD 与 CommonJS，浏览器全局来自第 19 行的顶层 `var qrcode`，因此 `window.qrcode` 可用；同时它可被 Node `require`，本轮用它做矩阵自校验。 |
| 已核对 | 全文件无 `fetch` / `XMLHttpRequest` / `eval` / `new Function` / `document.write` / `localStorage` / `importScripts`，纯编码器、不触网不碰 DOM。 |
| 未改动 | 原样落盘，未打补丁、未压缩、未混淆。 |
