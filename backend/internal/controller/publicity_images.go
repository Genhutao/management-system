package controller

// 宣传部「素材工坊」的真实上游接入：栗次元随机图 API（t.alcy.cc）。
//
//	GET https://t.alcy.cc/json?<分类>=<条数>
//	单条 → data 是对象 {"id","link"}；多条 → data 是数组，并额外给一个 links 字符串数组。
//
// 这里刻意做四件事：
//  1. 客户端只能从白名单分类里挑，永远拿不到一个可自由填写的 URL —— 否则这个接口
//     就成了任何登录用户都能用来探测内网的开放代理（SSRF）。
//  2. 图片由后端拉取并落到本地缓存，浏览器只访问本站同源地址。这与 2026-10-01
//     主页壁纸本地化是同一个口径：不让每个访客的 IP 暴露给第三方图床。
//  3. 上游是按分类随机 302，acg 分类给的是 mp4 视频，单张原图最大见过 4.7MB。
//     所以落盘前必须过扩展名、Content-Type 与体积三道关，不能拿到什么就往外发什么。
//  4. 上游不可达（校园网断外网是常态）时不编造数据：回落到最近一次成功批次里
//     **已经存在本地**的图，并在响应里如实标注 degraded 与原因。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	publicityImageUpstream = "栗次元随机图 API · t.alcy.cc"
	publicityDefaultCat    = "pc"
	publicityDefaultCount  = 6
	publicityMaxCount      = 12
	publicityMaxCacheFiles = 400 // 缓存文件数上限，超出按最旧删除
)

// 单张上限 8MB：上游实测有 4MB 的 5K 原图，再大的不像素材。声明成变量只为让测试能用小阈值
// 真正走一遍"超限即跳过"的分支，生产代码不会改它。
var publicityMaxImageBytes int64 = 8 << 20

// 缓存总体积上限：单按文件数封顶不够（400 张 4MB 就是 1.6GB），
// 值班机磁盘不大，超限后按最旧删除。
var publicityMaxCacheBytes int64 = 512 << 20

// 分类名与说明取自上游站点自己的分类文档，不自行改名；
// acg（随机 mp4 动图）是视频，故意不放进白名单。
type publicityCategory struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Note string `json:"note"`
}

var publicityCategories = []publicityCategory{
	{Key: "pc", Name: "PC 横图", Note: "桌面端横版壁纸"},
	{Key: "mp", Name: "移动竖图", Note: "手机竖版壁纸"},
	{Key: "moe", Name: "萌版横图", Note: "萌系横版壁纸"},
	{Key: "moemp", Name: "萌版竖图", Note: "萌系竖版壁纸"},
	{Key: "fj", Name: "风景横图", Note: "风景横版壁纸"},
	{Key: "bd", Name: "白底横图", Note: "白底素材图，适合做海报底图"},
	{Key: "tx", Name: "头像方图", Note: "方形头像图"},
	{Key: "ys", Name: "原神横图", Note: "原神横版壁纸"},
	{Key: "ysmp", Name: "原神竖图", Note: "原神竖版壁纸"},
	{Key: "lai", Name: "七濑胡桃", Note: "七濑胡桃表情包"},
	{Key: "xhl", Name: "小狐狸", Note: "小狐狸图包"},
}

var (
	publicityCatMap     = buildPublicityCatMap()
	publicityFileRe     = regexp.MustCompile(`^[0-9a-f]{64}\.(webp|jpe?g|png|gif)$`)
	publicityImageMu    sync.Mutex
	publicityAllowedExt = map[string]bool{"webp": true, "jpg": true, "jpeg": true, "png": true, "gif": true}
)

func buildPublicityCatMap() map[string]publicityCategory {
	m := make(map[string]publicityCategory, len(publicityCategories))
	for _, c := range publicityCategories {
		m[c.Key] = c
	}
	return m
}

// publicityImageBase 上游根地址。测试里指向假上游，部署时可用环境变量换镜像源。
func publicityImageBase() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLICITY_IMAGE_BASE")), "/"); v != "" {
		return v
	}
	return "https://t.alcy.cc"
}

func publicityImageCacheDir() string {
	if v := strings.TrimSpace(os.Getenv("PUBLICITY_IMAGE_CACHE_DIR")); v != "" {
		return v
	}
	return "./publicity-cache"
}

// fetchPublicityLinks 向 JSON 接口要 n 条链接，兼容 data 的对象/数组两种形状。
func fetchPublicityLinks(cat string, n int) ([]string, error) {
	base := publicityImageBase()
	u := fmt.Sprintf("%s/json?%s=%d", base, url.QueryEscape(cat), n)

	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("上游不可达: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 %d", res.StatusCode)
	}

	var payload struct {
		Code     int             `json:"code"`
		Category string          `json:"category"`
		Count    int             `json:"count"`
		Data     json.RawMessage `json:"data"`
		Links    []string        `json:"links"`
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取上游响应失败: %w", err)
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("上游响应不是合法 JSON: %w", err)
	}
	if payload.Code != 200 {
		return nil, fmt.Errorf("上游业务码 %d", payload.Code)
	}

	links := payload.Links
	if len(links) == 0 && len(payload.Data) > 0 {
		var arr []struct {
			ID   string `json:"id"`
			Link string `json:"link"`
		}
		if err := json.Unmarshal(payload.Data, &arr); err == nil {
			for _, item := range arr {
				links = append(links, item.Link)
			}
		} else {
			var one struct {
				Link string `json:"link"`
			}
			if err := json.Unmarshal(payload.Data, &one); err != nil {
				return nil, fmt.Errorf("上游 data 字段形状不认识: %w", err)
			}
			links = append(links, one.Link)
		}
	}
	return sanitizePublicityLinks(base, links), nil
}

// sanitizePublicityLinks 只保留"同一个上游主机 + /pic/ 路径 + 允许的扩展名"的链接。
// 上游哪怕被换成恶意响应，也指挥不动后端去请求别的地方。
func sanitizePublicityLinks(base string, links []string) []string {
	host, err := urlHostOf(base)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(links))
	for _, raw := range links {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Scheme != "https" && u.Scheme != "http" {
			continue
		}
		if !strings.EqualFold(u.Host, host) {
			continue
		}
		if !strings.HasPrefix(u.EscapedPath(), "/pic/") {
			continue
		}
		if !publicityAllowedExt[strings.ToLower(path.Ext(u.Path)[1:])] {
			continue
		}
		out = append(out, u.String())
	}
	return out
}

func urlHostOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}

// cachePublicityImage 把一张上游图落到本地缓存，返回本站可直链的文件名。
// 已存在则直接复用，不重复下载。
func cachePublicityImage(link string) (string, int64, error) {
	ext := strings.ToLower(path.Ext(link))
	if ext == "" {
		return "", 0, fmt.Errorf("链接没有扩展名")
	}
	sum := sha256.Sum256([]byte(link))
	name := hex.EncodeToString(sum[:]) + ext

	dir := publicityImageCacheDir()
	final := filepath.Join(dir, name)
	if st, err := os.Stat(final); err == nil && st.Size() > 0 {
		return name, st.Size(), nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("缓存目录不可用: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Get(link)
	if err != nil {
		return "", 0, fmt.Errorf("取图失败: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("取图返回 %d", res.StatusCode)
	}
	// acg 那类 mp4 会在这里被挡下：只收 image/*
	if !strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "image/") {
		return "", 0, fmt.Errorf("不是图片（Content-Type=%s）", res.Header.Get("Content-Type"))
	}

	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return "", 0, fmt.Errorf("临时文件创建失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// 多读 1 字节用于判断"是否超限"，而不是悄悄截断成一张坏图
	written, err := io.Copy(tmp, io.LimitReader(res.Body, publicityMaxImageBytes+1))
	if err != nil {
		tmp.Close()
		return "", 0, fmt.Errorf("下载中断: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("写入失败: %w", err)
	}
	if written > publicityMaxImageBytes {
		return "", 0, fmt.Errorf("超过单张上限 %dMB", publicityMaxImageBytes>>20)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", 0, fmt.Errorf("落盘失败: %w", err)
	}
	prunePublicityCache()
	return name, written, nil
}

// prunePublicityCache 按修改时间淘汰，避免缓存目录无限长大。
func prunePublicityCache() {
	dir := publicityImageCacheDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		mod  time.Time
		size int64
	}
	var files []item
	var total int64
	for _, e := range entries {
		if e.IsDir() || !publicityFileRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, item{filepath.Join(dir, e.Name()), info.ModTime(), info.Size()})
		total += info.Size()
	}
	if len(files) <= publicityMaxCacheFiles && total <= publicityMaxCacheBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	// 从最旧的开始删，直到文件数和总体积同时回到上限内
	excess := len(files) - publicityMaxCacheFiles
	for i := 0; i < len(files) && (excess > 0 || total > publicityMaxCacheBytes); i++ {
		if err := os.Remove(files[i].path); err != nil {
			continue
		}
		total -= files[i].size
		excess--
	}
}

// 最近一次成功批次：上游挂了就用它兜底，只回已经在本地的图。
func publicityLatestPath() string {
	return filepath.Join(publicityImageCacheDir(), "latest.json")
}

func readPublicityLatest() map[string][]string {
	out := map[string][]string{}
	body, err := os.ReadFile(publicityLatestPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(body, &out)
	return out
}

func writePublicityLatest(cat string, links []string) {
	dir := publicityImageCacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	all := readPublicityLatest()
	all[cat] = links
	body, err := json.Marshal(all)
	if err != nil {
		return
	}
	_ = os.WriteFile(publicityLatestPath(), body, 0o644)
}

type publicityImageItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Tag       string `json:"tag"`
	TagName   string `json:"tag_name"`
	URL       string `json:"url"`
	SourceURL string `json:"source_url"`
	Ext       string `json:"ext"`
	Bytes     int64  `json:"bytes"`
}

// GetRandomPublicityImages 依据分类取一批真实随机插画素材（后端代理 + 本地缓存）。
func (pc *PublicityController) GetRandomPublicityImages(c *gin.Context) {
	cat := strings.TrimSpace(c.DefaultQuery("tag", publicityDefaultCat))
	if cat == "" {
		cat = publicityDefaultCat
	}
	meta, ok := publicityCatMap[cat]
	if !ok {
		keys := make([]string, 0, len(publicityCategories))
		for _, item := range publicityCategories {
			keys = append(keys, item.Key)
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "不支持的图片分类：" + cat,
			"allowed": keys,
			"default": publicityDefaultCat,
		})
		return
	}

	count := publicityDefaultCount
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &count); err != nil {
			count = publicityDefaultCount
		}
	}
	if count < 1 {
		count = 1
	}
	if count > publicityMaxCount {
		count = publicityMaxCount
	}

	publicityImageMu.Lock()
	defer publicityImageMu.Unlock()

	upstream := "online"
	notice := ""
	links, ferr := fetchPublicityLinks(cat, count)
	if ferr != nil {
		upstream = "cache"
		notice = "上游暂时取不到（" + ferr.Error() + "），下面是最近一次成功获取且已存在服务器本地的素材"
		links = readPublicityLatest()[cat]
	} else {
		writePublicityLatest(cat, links)
	}

	items := make([]publicityImageItem, 0, len(links))
	for i, link := range links {
		name, size, err := cachePublicityImage(link)
		if err != nil {
			// 单张失败不影响其余：视频、超限、断流都在这里被跳过
			continue
		}
		items = append(items, publicityImageItem{
			ID:        fmt.Sprintf("%s_%s", cat, name[:12]),
			Title:     fmt.Sprintf("%s · 随机第 %d 张", meta.Name, i+1),
			Tag:       cat,
			TagName:   meta.Name,
			URL:       "/api/v1/publicity/images/file/" + name,
			SourceURL: link,
			Ext:       strings.ToLower(path.Ext(link)),
			Bytes:     size,
		})
	}

	// 界面最终是空的时候，三种原因要分开说，不能只给一个空画廊
	if len(items) == 0 {
		switch {
		case upstream == "cache":
			notice = "上游暂时取不到（" + ferr.Error() + "），服务器本地也还没有可用的缓存素材"
		case len(links) == 0:
			notice = "上游这次没有给出 " + cat + " 分类的可用链接，可换一个分类或稍后再试"
		default:
			notice = fmt.Sprintf("上游给了 %d 条链接，但取回的内容都不是可用图片（非图片或超过单张上限），已全部跳过", len(links))
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"tag":        cat,
		"tag_name":   meta.Name,
		"tag_note":   meta.Note,
		"total":      len(items),
		"items":      items,
		"categories": publicityCategories,
		"source":     publicityImageUpstream,
		"upstream":   upstream,
		"notice":     notice,
		"timestamp":  time.Now().Unix(),
	})
}

// ServePublicityImageFile 把缓存目录里的图以同源地址发给浏览器。
// 文件名必须是 64 位十六进制 + 白名单扩展名，因此不存在路径穿越，也列不出目录。
func (pc *PublicityController) ServePublicityImageFile(c *gin.Context) {
	name := c.Param("name")
	if !publicityFileRe.MatchString(name) {
		c.JSON(http.StatusNotFound, gin.H{"error": "素材不存在"})
		return
	}
	full := filepath.Join(publicityImageCacheDir(), name)
	if _, err := os.Stat(full); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "素材不存在或已被清理，请换一批"})
		return
	}
	c.File(full)
}

// ListPublicityImageCategories 单独给一份分类表，供前端渲染标签胶囊。
func (pc *PublicityController) ListPublicityImageCategories(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"categories": publicityCategories, "source": publicityImageUpstream})
}
