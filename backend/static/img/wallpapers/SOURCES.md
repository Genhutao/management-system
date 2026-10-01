# 主页壁纸来源与授权清单

主页轮播壁纸（`app.js` 的 `ANIME_WALLPAPERS`）于 2026-10-01 从 Unsplash CDN 下载并本地化到本目录，消除两件事：外网依赖（断网无壁纸）与访客浏览器直连第三方 CDN 的 IP 外泄。

## 授权依据

- 原始托管：Unsplash（images.unsplash.com），适用 **Unsplash License**（https://unsplash.com/license）。
- 该许可允许免费用于个人与商业项目，无需署名、无需付费、无需事先授权；禁止把图打包成竞争性图库服务出售、售卖未修改副本。本系统校内展示用途不触碰禁区。
- Unsplash+ 付费图走 plus.unsplash.com 域名，本清单所有图均不在其列。

## 文件对照

| 文件 | 原始 URL | 取回日期 | sha256（前 16 位） |
|---|---|---|---|
| w1.jpg | https://images.unsplash.com/photo-1578632767115-351597cf2477?w=1920&q=80&fit=crop | 2026-10-01 | cbb41fd8aae63116 |
| w2.jpg | https://images.unsplash.com/photo-1607604276583-eef5d076aa5f?w=1920&q=80&fit=crop | 2026-10-01 | 3110d1f598114fa2 |
| w3.jpg | https://images.unsplash.com/photo-1534447677768-be436bb09401?w=1920&q=80&fit=crop | 2026-10-01 | 96ce4117b54a5126 |
| w4.jpg | https://images.unsplash.com/photo-1563089145-599997674d42?w=1920&q=80&fit=crop | 2026-10-01 | 724213abbfba49ad |
| w5.jpg | https://images.unsplash.com/photo-1618005182384-a83a8bd57fbe?w=1920&q=80&fit=crop | 2026-10-01 | 4bef11f73d13fe2d |
| w6.jpg | https://images.unsplash.com/photo-1569701813229-33284b643e3c?w=1920&q=80&fit=crop | 2026-10-01 | efc083d57cb1ed91 |

下载参数统一为 `w=1920&q=80&fit=crop`（与原代码的差别仅少了 `auto=format`，得到确定性的 JPEG，不再按浏览器 Accept 协商格式）。

## 原列表第 7 张的下落

`photo-1579783902614-a3fb3927b675` 在取回当日（2026-10-01）已从源头损坏：CDN 返回 29 字节错误文本
`Error: Invalid or unsupported image data: Input buffer contains unsupported image format`，非图片内容。
这意味着本地化之前，线上轮播到该图时 `img.onload` 永不触发、轮播会静默卡住。本地化时将其从列表移除，现存 6 张。
