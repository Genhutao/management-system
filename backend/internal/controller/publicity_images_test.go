package controller

// 素材工坊真实上游接入的测试：假上游用 httptest 起，PUBLICITY_IMAGE_BASE 指过去，
// 因此这些用例不联网，也不碰真图床。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type galleryUpstream struct {
	srv       *httptest.Server
	closeOnce sync.Once
	jsonHits  int32
	imageHits int32
	count     int
	// imageBody 与 imageCT 控制 /pic/ 返回什么，用来演"不是图"和"超限"
	imageBody []byte
	imageCT   string
	failJSON  atomic.Bool
}

func (f *galleryUpstream) close() { f.closeOnce.Do(f.srv.Close) }

// links 必须挂在这个假上游自己的主机下：真代码只允许同源链接，
// 写死成别的主机会被消毒掉，测试就变成"什么都没测"。
func (f *galleryUpstream) links() []string {
	out := make([]string, 0, f.count)
	for i := 1; i <= f.count; i++ {
		out = append(out, fmt.Sprintf("%s/pic/pc/%d.webp", f.srv.URL, i))
	}
	return out
}

func newGalleryUpstream(t *testing.T, count int) *galleryUpstream {
	t.Helper()
	f := &galleryUpstream{count: count, imageBody: []byte("RIFFFAKEWEBPDATA"), imageCT: "image/webp"}
	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.jsonHits, 1)
		if f.failJSON.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		links := f.links()
		w.Header().Set("Content-Type", "application/json")
		// 上游形状：单条 data 是对象，多条 data 是数组并额外给 links
		if len(links) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200, "category": "pc", "count": 1,
				"data": map[string]string{"id": filepath.Base(links[0]), "link": links[0]},
			})
			return
		}
		data := make([]map[string]string, 0, len(links))
		for _, l := range links {
			data = append(data, map[string]string{"id": filepath.Base(l), "link": l})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "category": "pc", "count": len(links), "data": data, "links": links,
		})
	})
	mux.HandleFunc("/pic/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.imageHits, 1)
		w.Header().Set("Content-Type", f.imageCT)
		_, _ = w.Write(f.imageBody)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.close)
	return f
}

func newPublicityRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	pc := &PublicityController{}
	r.GET("/api/v1/publicity/images", pc.GetRandomPublicityImages)
	r.GET("/api/v1/publicity/images/file/:name", pc.ServePublicityImageFile)
	r.GET("/api/v1/publicity/images/categories", pc.ListPublicityImageCategories)
	return r
}

func decodeGallery(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v / %s", err, w.Body.String())
	}
	return out
}

func galleryItems(out map[string]any) []any {
	items, _ := out["items"].([]any)
	return items
}

func setupPublicityFixture(t *testing.T, count int) (*galleryUpstream, *gin.Engine) {
	t.Helper()
	f := newGalleryUpstream(t, count)
	t.Setenv("PUBLICITY_IMAGE_BASE", f.srv.URL)
	t.Setenv("PUBLICITY_IMAGE_CACHE_DIR", t.TempDir())
	return f, newPublicityRouter()
}

// 白名单：界面上不该再出现后端不认的分类，acg（mp4）也不该可选。
func TestPublicityImagesRejectsUnknownCategory(t *testing.T) {
	f, r := setupPublicityFixture(t, 1)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=ink", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知分类应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	out := decodeGallery(t, w)
	if strings.Contains(fmt.Sprint(out["allowed"]), "acg") {
		t.Fatalf("acg 是 mp4 动图，不该出现在可选分类里: %v", out["allowed"])
	}
	if atomic.LoadInt32(&f.jsonHits) != 0 {
		t.Fatal("分类不合法时不该去请求上游")
	}
}

// 上游 data 单条是对象、多条是数组，两种形状都要能吃下。
func TestPublicityImagesHandlesBothJSONShapes(t *testing.T) {
	f, r := setupPublicityFixture(t, 1)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=1", nil))
	if w.Code != http.StatusOK || len(galleryItems(decodeGallery(t, w))) != 1 {
		t.Fatalf("单条对象形状没解析出来: %d %s", w.Code, w.Body.String())
	}
	f.close()

	_, r2 := setupPublicityFixture(t, 3)
	w2 := httptest.NewRecorder()
	r2.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=3", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("多条应 200，实际 %d: %s", w2.Code, w2.Body.String())
	}
	if got := len(galleryItems(decodeGallery(t, w2))); got != 3 {
		t.Fatalf("数组 + links 形状应解析出 3 张，实际 %d: %s", got, w2.Body.String())
	}
}

// 消毒：主机不是上游的、扩展名不在白名单的链接，一律不请求也不落盘。
func TestPublicityImagesSanitizesForeignAndVideoLinks(t *testing.T) {
	var (
		foreignHits int32
		imageHits   int32
	)
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&foreignHits, 1)
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("should-never-be-fetched"))
	}))
	t.Cleanup(evil.Close)

	var up *httptest.Server
	up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"code":200,"category":"pc","count":4,"links":[`+
				`"%s/pic/pc/ok.webp",`+ // 正常
				`"%s/pic/acg/24146.mp4",`+ // 视频扩展名
				`"%s/pic/pc/stolen.webp",`+ // 别的主机（可达，被请求到就计数）
				`"https://attacker.example/pic/pc/x.webp"]}`, // 别的主机（不可达）
				up.URL, up.URL, evil.URL)
		case strings.HasPrefix(r.URL.Path, "/pic/"):
			atomic.AddInt32(&imageHits, 1)
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write([]byte("RIFFFAKEWEBPDATA"))
		}
	}))
	t.Cleanup(up.Close)

	t.Setenv("PUBLICITY_IMAGE_BASE", up.URL)
	t.Setenv("PUBLICITY_IMAGE_CACHE_DIR", t.TempDir())

	r := newPublicityRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=4", nil))
	items := galleryItems(decodeGallery(t, w))
	if len(items) != 1 {
		t.Fatalf("4 条链接里只该留下那一张合法图，实际 %d: %s", len(items), w.Body.String())
	}
	if got := atomic.LoadInt32(&imageHits); got != 1 {
		t.Fatalf("不该为被丢弃的链接发起取图请求，实际请求 %d 次", got)
	}
	if got := atomic.LoadInt32(&foreignHits); got != 0 {
		t.Fatalf("消毒没生效，白名单外主机被请求了 %d 次", got)
	}
}

// 内容不是 image/* 时（上游 acg 分类真会给 mp4），不能把视频当图发出去。
func TestPublicityImagesSkipsNonImageContentType(t *testing.T) {
	f := newGalleryUpstream(t, 1)
	f.imageCT = "video/mp4"

	r := newPublicityRouter()
	w := httptest.NewRecorder()
	t.Setenv("PUBLICITY_IMAGE_BASE", f.srv.URL)
	t.Setenv("PUBLICITY_IMAGE_CACHE_DIR", t.TempDir())
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=1", nil))
	if n := len(galleryItems(decodeGallery(t, w))); n != 0 {
		t.Fatalf("Content-Type 是 video/mp4 时不该出图，实际 %d 张", n)
	}
}

// 超限必须整张跳过，而不是悄悄截断成一张坏图。
func TestPublicityImagesSkipsOversizedImage(t *testing.T) {
	f := newGalleryUpstream(t, 1)
	f.imageBody = []byte(strings.Repeat("A", 4096))

	old := publicityMaxImageBytes
	publicityMaxImageBytes = 1024
	t.Cleanup(func() { publicityMaxImageBytes = old })

	r := newPublicityRouter()
	w := httptest.NewRecorder()
	t.Setenv("PUBLICITY_IMAGE_BASE", f.srv.URL)
	t.Setenv("PUBLICITY_IMAGE_CACHE_DIR", t.TempDir())
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=1", nil))
	if n := len(galleryItems(decodeGallery(t, w))); n != 0 {
		t.Fatalf("超过单张上限的图应被跳过，实际 %d 张", n)
	}
}

// 同一批链接第二次请求不该再向上游取图（缓存复用）。
func TestPublicityImagesReusesLocalCache(t *testing.T) {
	f, r := setupPublicityFixture(t, 2)

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=2", nil))
		if n := len(galleryItems(decodeGallery(t, w))); n != 2 {
			t.Fatalf("第 %d 次应有 2 张，实际 %d", i+1, n)
		}
	}
	if got := atomic.LoadInt32(&f.imageHits); got != 2 {
		t.Fatalf("两轮共 2 张图，只该各下载一次，实际取图 %d 次", got)
	}
}

// 上游挂了不编造：回落到最近一次成功批次里已在本地的图，并如实标注。
func TestPublicityImagesDegradesToCachedBatch(t *testing.T) {
	f, r := setupPublicityFixture(t, 1)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=1", nil))
	first := decodeGallery(t, w)
	if first["upstream"] != "online" {
		t.Fatalf("首轮应标记 online，实际 %v", first["upstream"])
	}
	items, _ := first["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("首轮应有 1 张，实际 %d: %s", len(items), w.Body.String())
	}
	item0, _ := items[0].(map[string]any)
	fileURL, _ := item0["url"].(string)
	if fileURL == "" {
		t.Fatalf("响应里没给出同源文件地址: %v", item0)
	}

	f.failJSON.Store(true)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=1", nil))
	second := decodeGallery(t, w2)
	if second["upstream"] != "cache" {
		t.Fatalf("上游失败应标记 cache，实际 %v: %s", second["upstream"], w2.Body.String())
	}
	if notice, _ := second["notice"].(string); !strings.Contains(notice, "上游") {
		t.Fatalf("降级时必须说明原因，实际 notice=%q", notice)
	}
	if n := len(galleryItems(second)); n != 1 {
		t.Fatalf("降级时应回落到已缓存的那 1 张，实际 %d", n)
	}

	// 回落出来的图必须还能取到
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, fileURL, nil))
	if w3.Code != http.StatusOK || !strings.HasPrefix(w3.Header().Get("Content-Type"), "image/") {
		t.Fatalf("缓存图应能同源取回，实际 %d / %s", w3.Code, w3.Header().Get("Content-Type"))
	}
}

// 断网冷启动：上游取不到、本地又还没有缓存时，不能谎称"下面是本地素材"。
func TestPublicityImagesExplainsEmptyCache(t *testing.T) {
	f, r := setupPublicityFixture(t, 2)
	f.failJSON.Store(true)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=2", nil))
	out := decodeGallery(t, w)
	if n := len(galleryItems(out)); n != 0 {
		t.Fatalf("冷启动无缓存时应为空，实际 %d 张", n)
	}
	notice, _ := out["notice"].(string)
	if !strings.Contains(notice, "上游") || !strings.Contains(notice, "缓存") {
		t.Fatalf("必须同时说明上游失败与本地无缓存，实际 notice=%q", notice)
	}
}

// 上游"成功"但所有链接都被消毒掉时，界面不该只显示一个没来由的空画廊。
func TestPublicityImagesExplainsAllLinksDropped(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"category":"pc","count":2,"links":[` +
			`"https://other.example/pic/pc/a.webp","https://other.example/pic/pc/b.webp"]}`))
	}))
	t.Cleanup(up.Close)
	t.Setenv("PUBLICITY_IMAGE_BASE", up.URL)
	t.Setenv("PUBLICITY_IMAGE_CACHE_DIR", t.TempDir())

	w := httptest.NewRecorder()
	newPublicityRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=2", nil))
	out := decodeGallery(t, w)
	if out["upstream"] != "online" {
		t.Fatalf("上游本身是通的，应标记 online，实际 %v", out["upstream"])
	}
	if n := len(galleryItems(out)); n != 0 {
		t.Fatalf("外链不该被采纳，实际 %d 张", n)
	}
	if notice, _ := out["notice"].(string); !strings.Contains(notice, "链接") {
		t.Fatalf("应说明链接被判定不可用，实际 notice=%q", notice)
	}
}

// 真上游冒烟测试：默认跳过，不拖慢也不依赖 CI 的联网能力。
// 手动跑一次：PUBLICITY_LIVE_UPSTREAM_TEST=1 go test ./internal/controller/ -run Live -v
func TestPublicityImagesLiveUpstream(t *testing.T) {
	if os.Getenv("PUBLICITY_LIVE_UPSTREAM_TEST") != "1" {
		t.Skip("默认不联网；设置 PUBLICITY_LIVE_UPSTREAM_TEST=1 才向真上游取图")
	}
	cache := t.TempDir()
	t.Setenv("PUBLICITY_IMAGE_CACHE_DIR", cache)

	w := httptest.NewRecorder()
	newPublicityRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images?tag=pc&limit=3", nil))
	out := decodeGallery(t, w)
	if w.Code != http.StatusOK || out["upstream"] != "online" {
		t.Fatalf("真上游应 200 且标记 online，实际 %d / %v: %s", w.Code, out["upstream"], w.Body.String())
	}
	items := galleryItems(out)
	if len(items) == 0 {
		t.Fatalf("真上游没取到任何图: %s", w.Body.String())
	}
	t.Logf("上游 %s：取回 %d 张", out["source"], len(items))

	r := newPublicityRouter()
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		fileURL, _ := item["url"].(string)
		if !strings.HasPrefix(fileURL, "/api/v1/publicity/images/file/") {
			t.Fatalf("素材地址必须是同源直链，实际 %q", fileURL)
		}
		name := strings.TrimPrefix(fileURL, "/api/v1/publicity/images/file/")
		if !publicityFileRe.MatchString(name) {
			t.Fatalf("落盘文件名不符合约定: %q", name)
		}
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, fileURL, nil))
		if w2.Code != http.StatusOK {
			t.Fatalf("同源取图失败 %s -> %d", fileURL, w2.Code)
		}
		body := w2.Body.Bytes()
		ct := w2.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "image/") {
			t.Fatalf("真上游内容类型不是图片: %s", ct)
		}
		if float64(len(body)) != item["bytes"].(float64) {
			t.Fatalf("字节数对不上：响应声明 %v，实际 %d", item["bytes"], len(body))
		}
		if int64(len(body)) > publicityMaxImageBytes {
			t.Fatalf("缓存了超过单张上限的图：%d 字节 > %d", len(body), publicityMaxImageBytes)
		}
		t.Logf("  %s / %s -> %s · %.1f KB", item["tag_name"], item["title"], ct, float64(len(body))/1024)
	}
}

// 缓存淘汰：数量与总体积两个上限都要生效，且不能碰到 latest.json 这类非缓存文件。
func TestPrunePublicityCacheKeepsBothCaps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PUBLICITY_IMAGE_CACHE_DIR", dir)

	old := publicityMaxCacheBytes
	publicityMaxCacheBytes = 2500
	t.Cleanup(func() { publicityMaxCacheBytes = old })

	names := make([]string, 0, 4)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("%064x.webp", i)
		full := filepath.Join(dir, name)
		if err := os.WriteFile(full, []byte(strings.Repeat("A", 1000)), 0o644); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(full, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	keep := filepath.Join(dir, "latest.json")
	if err := os.WriteFile(keep, []byte(`{"pc":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	prunePublicityCache()

	for _, name := range names[:2] { // 最旧的两个该被清掉
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("最旧的缓存文件 %s 应被淘汰", name)
		}
	}
	for _, name := range names[2:] { // 最新的两个该留下
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("较新的缓存文件 %s 不该被删: %v", name, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("latest.json 不是缓存图，不该被扫到: %v", err)
	}
}

// 文件路由：只认 64 位十六进制 + 白名单扩展名，路径穿越与乱名字一律拿不到东西。
func TestServePublicityImageFileRejectsBadNames(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("PUBLICITY_IMAGE_CACHE_DIR", cache)
	// 在缓存目录的上一级放一个哨兵文件，穿越成功就能读到它
	sentinel := filepath.Join(filepath.Dir(cache), "publicity-traversal-sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("module xgh-system"), 0o644); err == nil {
		t.Cleanup(func() { _ = os.Remove(sentinel) })
	}

	r := newPublicityRouter()
	for _, name := range []string{
		"..%2f..%2f..%2fgo.mod",
		"not-hash.webp",
		strings.Repeat("z", 64) + ".webp",
		strings.Repeat("a", 64) + ".svg",
		strings.Repeat("a", 64) + ".webp", // 合法形状但文件不存在
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images/file/"+name, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("文件名 %q 应 404，实际 %d: %s", name, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "module xgh-system") {
			t.Fatalf("路径穿越读到了仓库文件: %q", name)
		}
	}
}

// 分类表接口给前端的必须是上游真实分类，且不含 acg。
func TestListPublicityImageCategories(t *testing.T) {
	w := httptest.NewRecorder()
	newPublicityRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/images/categories", nil))
	var out struct {
		Categories []publicityCategory `json:"categories"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Categories) == 0 {
		t.Fatal("分类表为空")
	}
	for _, c := range out.Categories {
		if c.Key == "acg" || c.Key == "" || c.Name == "" {
			t.Fatalf("分类表异常: %+v", c)
		}
	}
}
