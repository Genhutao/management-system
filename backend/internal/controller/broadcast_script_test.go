package controller

// 播音组 AI 讲稿的断言重点不是"稿子写得好不好"，而是四件能在代码里钉死的事：
// 天气一天只打一次上游；取不到天气时绝不编一句假天气；三路检索全挂就不出稿；
// 以及两把密钥（检索与文本引擎）在任何响应和审计里都不出现。
// 三个上游全部用 httptest 假实现，靠 FEED_*_ENDPOINT 环境变量指过去，测试不联网。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

const (
	fakeSearchKey  = "tvly-fake-search-key-not-a-real-secret"
	fakeTextAIKey  = "sk-fake-text-engine-key"
	fakeSearchBody = `{"results":[
	  {"title":"安理会就地区冲突召开紧急会议","url":"https://news.example/a1","content":"各方呼吁立即停火并开放人道主义走廊，秘书长警告平民处境持续恶化。"},
	  {"title":"前三季度国民经济数据发布","url":"https://news.example/a2","content":"国内生产总值同比增长稳定，制造业投资与消费市场回升明显，就业总体平稳。"},
	  {"title":"新一代人工智能芯片集中发布","url":"https://news.example/a3","content":"多家企业同日发布新款芯片与开源大模型，算力效率提升明显，业内预计成本下降加快落地。"}]}`
)

// 模型该产出的三条正文：合计 147 字，落在使用方要求的 120–150 区间内
var fakeNewsBody = strings.Join([]string{
	"1. 联合国安理会就地区冲突召开紧急会议，各方呼吁立即停火并开放人道救援走廊，警告平民处境持续恶化。",
	"2. 统计部门发布前三季度国民经济数据，国内生产总值同比增长保持稳定，制造业投资与消费市场回升明显。",
	"3. 多家科技企业同日发布新一代人工智能芯片与开源大模型，业内认为算力成本下降将加快应用落地。",
}, "\n")

type broadcastUpstreams struct {
	searchHits  int32
	weatherHits int32
	aiHits      int32
	aiEndpoint  string
	searchAuth  atomic.Value // string：最后一次收到的 Authorization
	searchFail  atomic.Bool
	weatherFail atomic.Bool
	aiBody      atomic.Value // string：文本引擎要返回的 content
}

func newBroadcastUpstreams(t *testing.T) *broadcastUpstreams {
	t.Helper()
	u := &broadcastUpstreams{}
	u.aiBody.Store(fakeNewsBody)

	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&u.searchHits, 1)
		u.searchAuth.Store(r.Header.Get("Authorization"))
		if u.searchFail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("upstream boom"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeSearchBody))
	}))

	weather := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&u.weatherHits, 1)
		if u.weatherFail.Load() {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// temperature 故意给数字而不是字符串：上游就是这么发的，收不住就会整个功能报错
		_, _ = w.Write([]byte(`{"province":"浙江省","city":"杭州市","weather":"阴","temperature":22,"wind_direction":"西北风","wind_power":"2级","humidity":79}`))
	}))

	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&u.aiHits, 1)
		content, _ := u.aiBody.Load().(string)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
	}))

	t.Cleanup(func() { search.Close(); weather.Close(); engine.Close() })
	t.Setenv("FEED_SEARCH_ENDPOINT", search.URL)
	t.Setenv("FEED_WEATHER_ENDPOINT", weather.URL)
	u.aiEndpoint = engine.URL
	return u
}

func setupBroadcastFixture(t *testing.T) *broadcastUpstreams {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AIConfig{}, &model.BroadcastFeedConfig{},
		&model.BroadcastWeatherCache{}, &model.OperationLog{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	prev := repository.DB
	repository.DB = db
	t.Cleanup(func() {
		repository.DB = prev
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	u := newBroadcastUpstreams(t)

	if err := db.Create(&model.BroadcastFeedConfig{
		WeatherCity: "杭州市", SearchAPIKey: fakeSearchKey, SearchEnabled: true,
	}).Error; err != nil {
		t.Fatalf("写入播报数据源配置失败: %v", err)
	}
	if err := db.Create(&model.AIConfig{
		ConfigKey: "text_engine", DisplayName: "文本引擎", Provider: "openai_compatible",
		Endpoint: u.aiEndpoint + "/v1", APIKey: fakeTextAIKey, ModelName: "fake-model",
		Temperature: 0.4, MaxTokens: 600, IsEnabled: true,
	}).Error; err != nil {
		t.Fatalf("写入文本引擎失败: %v", err)
	}
	for _, role := range []string{model.RoleTechAdmin, model.RoleMember} {
		if err := db.Create(&model.User{
			Username: "u_" + role, RealName: role, Role: role, Status: "active",
		}).Error; err != nil {
			t.Fatalf("写入用户失败: %v", err)
		}
	}
	return u
}

func broadcastRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	pc := &PublicityController{}
	r.POST("/api/v1/publicity/broadcast/ai-script", pc.GenerateBroadcastScript)
	r.GET("/api/v1/publicity/broadcast/feed-config", pc.GetBroadcastFeedConfig)
	r.PUT("/api/v1/publicity/broadcast/feed-config", pc.UpdateBroadcastFeedConfig)
	return r
}

func callGenerate(t *testing.T, r *gin.Engine) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/publicity/broadcast/ai-script", strings.NewReader("{}")))
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v / %s", err, w.Body.String())
	}
	return out
}

// 正常路径：开场白与天气由代码拼，模型只写三条正文；响应里不许出现任何密钥。
func TestBroadcastScriptHappyPath(t *testing.T) {
	u := setupBroadcastFixture(t)
	w := callGenerate(t, broadcastRouter())
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	out := decodeJSON(t, w)
	script, _ := out["script"].(string)

	if !strings.HasPrefix(script, "世界新闻早知道，今日新闻我来报。学管会带你了解今日新闻。") {
		t.Errorf("开场白不符合规定格式:\n%s", script)
	}
	if !strings.Contains(script, "下面是天气预报：") {
		t.Errorf("缺少天气预报段落:\n%s", script)
	}
	if !strings.Contains(script, "温度22℃") || !strings.Contains(script, "杭州市") {
		t.Errorf("天气句必须来自真实接口字段:\n%s", script)
	}
	if out["weather_source"] != "online" {
		t.Errorf("首轮天气应标记 online，实际 %v", out["weather_source"])
	}
	if out["saved"] != false {
		t.Errorf("稿子不得落库，saved 应为 false，实际 %v", out["saved"])
	}
	refs, _ := out["news_refs"].([]any)
	if len(refs) == 0 {
		t.Errorf("必须回传素材出处供人工核对")
	}
	if n, _ := out["news_words"].(float64); n < 120 || n > 150 {
		t.Errorf("正文应落在 120–150 字，实际 %v", out["news_words"])
	}
	if got := atomic.LoadInt32(&u.searchHits); got != 3 {
		t.Errorf("三类主题应各检索一次，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(&u.weatherHits); got != 1 {
		t.Errorf("天气只该打一次上游，实际 %d 次", got)
	}
	body := w.Body.String()
	for _, secret := range []string{fakeSearchKey, fakeTextAIKey} {
		if strings.Contains(body, secret) {
			t.Fatalf("响应里泄露了密钥: %s", secret[:12]+"…")
		}
	}
	if auth, _ := u.searchAuth.Load().(string); auth != "Bearer "+fakeSearchKey {
		t.Errorf("检索请求没带上库里的密钥（说明解密链路断了），实际 %q", auth)
	}
}

// 按日缓存：同一天第二次生成绝不能再打天气上游，且要在提示里说清用的是缓存。
func TestBroadcastWeatherCachedForTheDay(t *testing.T) {
	u := setupBroadcastFixture(t)
	r := broadcastRouter()

	first := decodeJSON(t, callGenerate(t, r))
	if first["weather_source"] != "online" {
		t.Fatalf("首轮应现取，实际 %v", first["weather_source"])
	}
	second := decodeJSON(t, callGenerate(t, r))
	if second["weather_source"] != "cache" {
		t.Fatalf("当天第二轮应读缓存，实际 %v", second["weather_source"])
	}
	if got := atomic.LoadInt32(&u.weatherHits); got != 1 {
		t.Fatalf("两轮生成天气只该打一次上游，实际 %d 次", got)
	}
	warnings, _ := second["warnings"].([]any)
	joined := ""
	for _, x := range warnings {
		joined += fmt.Sprint(x)
	}
	if !strings.Contains(joined, "缓存") {
		t.Errorf("用了缓存必须如实提示，实际 warnings=%v", warnings)
	}
}

// 天气取不到时：宁可写"待补充"，也不能拿一句套话冒充真天气。
func TestBroadcastScriptNeverFabricatesWeather(t *testing.T) {
	u := setupBroadcastFixture(t)
	u.weatherFail.Store(true)

	w := callGenerate(t, broadcastRouter())
	if w.Code != http.StatusOK {
		t.Fatalf("天气失败不该挡住整篇稿子，实际 %d: %s", w.Code, w.Body.String())
	}
	out := decodeJSON(t, w)
	script, _ := out["script"].(string)
	if !strings.Contains(script, "今日天气数据暂未取到") {
		t.Errorf("天气缺失时应写明待补充:\n%s", script)
	}
	if strings.Contains(script, "℃") {
		t.Errorf("没取到天气却出现了温度，等于编造:\n%s", script)
	}
	if out["weather_source"] != "none" {
		t.Errorf("weather_source 应为 none，实际 %v", out["weather_source"])
	}
}

// 三路检索全挂：没有素材就不出稿，不能拿模板话凑一篇"今日新闻"。
func TestBroadcastScriptRefusesWhenAllNewsFail(t *testing.T) {
	u := setupBroadcastFixture(t)
	u.searchFail.Store(true)

	w := callGenerate(t, broadcastRouter())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("全挂应 502，实际 %d: %s", w.Code, w.Body.String())
	}
	out := decodeJSON(t, w)
	if out["code"] != "news_upstream_failed" {
		t.Errorf("错误码应为 news_upstream_failed，实际 %v", out["code"])
	}
	if _, has := out["script"]; has {
		t.Errorf("失败时不得返回任何稿子: %v", out["script"])
	}
}

// 模型自由发挥（超长）时判不合规，而不是照单全收。
func TestBroadcastScriptRejectsOffSpecOutput(t *testing.T) {
	u := setupBroadcastFixture(t)
	u.aiBody.Store("1. " + strings.Repeat("某地发生了一系列需要长期关注的复杂情况并且各方表态不一。", 20))

	w := callGenerate(t, broadcastRouter())
	if w.Code != http.StatusBadGateway {
		t.Fatalf("超长正文应被拒绝，实际 %d: %s", w.Code, w.Body.String())
	}
	if code := decodeJSON(t, w)["code"]; code != "ai_output_off_spec" {
		t.Errorf("错误码应为 ai_output_off_spec，实际 %v", code)
	}
}

// 没配检索密钥 / 没配文本引擎：分别说清缺的是哪一环，不去打上游也不演一遍。
func TestBroadcastScriptReportsMissingDependencies(t *testing.T) {
	setupBroadcastFixture(t)
	r := broadcastRouter()

	repository.DB.Model(&model.BroadcastFeedConfig{}).Where("1 = 1").Update("search_enabled", false)
	w := callGenerate(t, r)
	if w.Code != http.StatusBadRequest || decodeJSON(t, w)["code"] != "feed_not_configured" {
		t.Fatalf("未开检索应 400 feed_not_configured，实际 %d: %s", w.Code, w.Body.String())
	}

	repository.DB.Model(&model.BroadcastFeedConfig{}).Where("1 = 1").Update("search_enabled", true)
	repository.DB.Model(&model.AIConfig{}).Where("config_key = ?", "text_engine").Update("is_enabled", false)
	w2 := callGenerate(t, r)
	if w2.Code != http.StatusServiceUnavailable || decodeJSON(t, w2)["code"] != "ai_not_configured" {
		t.Fatalf("文本引擎未启用应 503 ai_not_configured，实际 %d: %s", w2.Code, w2.Body.String())
	}
}

func userByRole(t *testing.T, role string) model.User {
	t.Helper()
	var u model.User
	if err := repository.DB.Where("role = ?", role).First(&u).Error; err != nil {
		t.Fatalf("找不到 %s 测试账号: %v", role, err)
	}
	return u
}

// 配置读写：GET 永远只给脱敏形态；PUT 只有技术维护组能做，且审计里不留密钥。
func TestBroadcastFeedConfigGuardsKeyAndRole(t *testing.T) {
	setupBroadcastFixture(t)
	r := broadcastRouter()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/publicity/broadcast/feed-config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("读配置应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), fakeSearchKey) {
		t.Fatal("配置接口泄露了检索密钥明文")
	}
	out := decodeJSON(t, w)
	if out["has_key"] != true || out["weather_city"] != "杭州市" {
		t.Fatalf("应回 has_key/城市名，实际 %v", out)
	}
	mask, _ := out["key_mask"].(string)
	if mask == "" || strings.Contains(mask, fakeSearchKey) {
		t.Errorf("key_mask 必须是脱敏串，实际 %q", mask)
	}

	tech := userByRole(t, model.RoleTechAdmin)
	member := userByRole(t, model.RoleMember)

	// 部员：Casbin 对 /publicity/* 连 PUT 一起放开，所以这道判定必须在这里
	deniedW := httptest.NewRecorder()
	denied, _ := gin.CreateTestContext(deniedW)
	denied.Request = httptest.NewRequest(http.MethodPut, "/api/v1/publicity/broadcast/feed-config", strings.NewReader(`{"weather_city":"北京市"}`))
	denied.Request.Header.Set("Content-Type", "application/json")
	denied.Set("user_id", member.ID)
	pc := &PublicityController{}
	pc.UpdateBroadcastFeedConfig(denied)
	if deniedW.Code != http.StatusForbidden {
		t.Fatalf("部员不应能改播报数据源，实际 %d: %s", deniedW.Code, deniedW.Body.String())
	}

	allowedW := httptest.NewRecorder()
	allowed, _ := gin.CreateTestContext(allowedW)
	allowed.Request = httptest.NewRequest(http.MethodPut, "/api/v1/publicity/broadcast/feed-config", strings.NewReader(`{"weather_city":"北京市","search_enabled":true}`))
	allowed.Request.Header.Set("Content-Type", "application/json")
	allowed.Set("user_id", tech.ID)
	pc.UpdateBroadcastFeedConfig(allowed)
	if allowedW.Code != http.StatusOK {
		t.Fatalf("技术组改配置应 200，实际 %d: %s", allowedW.Code, allowedW.Body.String())
	}
	if strings.Contains(allowedW.Body.String(), fakeSearchKey) {
		t.Fatal("改配置的响应也不该带出密钥")
	}

	var cfg model.BroadcastFeedConfig
	if err := repository.DB.First(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	if cfg.WeatherCity != "北京市" || cfg.SearchAPIKey != fakeSearchKey {
		t.Fatalf("留空应保留原密钥、城市要改掉，实际 city=%q key=%q", cfg.WeatherCity, cfg.SearchAPIKey)
	}

	var logs []model.OperationLog
	repository.DB.Where("action = ?", "broadcast.feed_config").Find(&logs)
	if len(logs) == 0 {
		t.Fatal("改配置必须留审计")
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, fakeSearchKey) {
			t.Fatalf("审计明细里出现了密钥: %s", l.Detail)
		}
	}
	if !strings.Contains(logs[len(logs)-1].Detail, "保持不变") {
		t.Errorf("未提交密钥时审计要写明保持不变，实际 %q", logs[len(logs)-1].Detail)
	}
}

// 显式清空密钥：不给 clear_key 就永远保留，避免"界面上留空 = 悄悄抹掉密钥"。
func TestBroadcastFeedConfigClearKey(t *testing.T) {
	setupBroadcastFixture(t)
	tech := userByRole(t, model.RoleTechAdmin)

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/publicity/broadcast/feed-config", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user_id", tech.ID)
		(&PublicityController{}).UpdateBroadcastFeedConfig(c)
		return rec
	}

	kept := put(`{"weather_city":"北京市"}`)
	if kept.Code != http.StatusOK || kept.Body.String() == "" {
		t.Fatalf("留空应保留原密钥，实际 %d: %s", kept.Code, kept.Body.String())
	}
	var cfg model.BroadcastFeedConfig
	if err := repository.DB.First(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	if cfg.SearchAPIKey != fakeSearchKey {
		t.Fatalf("密钥被误删了: %q", cfg.SearchAPIKey)
	}

	cleared := put(`{"clear_key":true}`)
	if cleared.Code != http.StatusOK {
		t.Fatalf("清空应 200，实际 %d: %s", cleared.Code, cleared.Body.String())
	}
	if err := repository.DB.First(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(cfg.SearchAPIKey) != "" {
		t.Fatalf("clear_key 没清掉密钥: %q", cfg.SearchAPIKey)
	}
	out := decodeJSON(t, cleared)
	if out["has_key"] != false {
		t.Errorf("清空后 has_key 应为 false，实际 %v", out["has_key"])
	}
	if strings.Contains(cleared.Body.String(), fakeSearchKey) {
		t.Error("响应里不应出现密钥")
	}
}

func TestBroadcastNewsBodyLengthSanity(t *testing.T) {
	// 夹具本身要落在要求区间内，否则上面的字数断言是在测一个假设定
	n := utf8.RuneCountInString(strings.ReplaceAll(fakeNewsBody, "\n", ""))
	if n < 120 || n > 150 {
		t.Fatalf("夹具正文 %d 字，不在 120–150 内，用例失去意义", n)
	}
}
