// =============================================================================
// 学管会管理系统 · 全屏壁纸模式与 NewAPI 极简工作台驱动核心脚本
// =============================================================================

const API_BASE = "/api/v1";

const state = {
  user: JSON.parse(localStorage.getItem("xgh_user") || "null"),
  currentTab: "dorm",
  activeExam: null,
  loginMode: "password",
};

// 页面加载启动
document.addEventListener("DOMContentLoaded", () => {
  startSystemClock();
  initTouchSwipe();
  initModalAndDrawerGuards();
  initMouseAuroraTracker();
  initAnimeWallpaperEngine();

  // 显式绑定首页关键按钮事件，双重保障点击响应
  const btnRec = document.getElementById("btn-home-recruit");
  if (btnRec) btnRec.addEventListener("click", openRecruitModal);

  const btnLog = document.getElementById("btn-home-login");
  if (btnLog) btnLog.addEventListener("click", openLoginModal);

  const btnDrw = document.getElementById("btn-home-drawer");
  if (btnDrw) btnDrw.addEventListener("click", toggleSideDrawer);

  // 移动端环境检测与持久化三要素静默直登
  checkMobileApkEnvironment();
  
  if (state.user) {
    showWorkspaceView();
    renderUserSlot();
    routeUserToDefault();
    syncIdentityFromServer({ force: true });
  } else {
    showWallpaperView();
  }
});

// -----------------------------------------------------------------------------
// 首页月亮光幕视觉系统：鼠标移动追踪与明暗光幕跟随
// -----------------------------------------------------------------------------
function initMouseAuroraTracker() {
  const landing = document.getElementById("view-wallpaper-landing");
  if (!landing) return;

  let currentX = 50;
  let currentY = 40;
  let targetX = 50;
  let targetY = 40;

  window.addEventListener("mousemove", (e) => {
    const x = (e.clientX / window.innerWidth) * 100;
    const y = (e.clientY / window.innerHeight) * 100;
    targetX = Math.round(x);
    targetY = Math.round(y);
  }, { passive: true });

  // 使用 requestAnimationFrame 平滑缓动
  function updateAuroraPosition() {
    currentX += (targetX - currentX) * 0.08;
    currentY += (targetY - currentY) * 0.08;
    landing.style.setProperty("--mouse-x", `${currentX.toFixed(2)}%`);
    landing.style.setProperty("--mouse-y", `${currentY.toFixed(2)}%`);
    requestAnimationFrame(updateAuroraPosition);
  }
  requestAnimationFrame(updateAuroraPosition);
}

// -----------------------------------------------------------------------------
// 首页二次元壁纸 API 随机轮播引擎
// -----------------------------------------------------------------------------
const ANIME_WALLPAPERS = [
  "https://images.unsplash.com/photo-1578632767115-351597cf2477?w=1920&q=80&auto=format&fit=crop",
  "https://images.unsplash.com/photo-1607604276583-eef5d076aa5f?w=1920&q=80&auto=format&fit=crop",
  "https://images.unsplash.com/photo-1534447677768-be436bb09401?w=1920&q=80&auto=format&fit=crop",
  "https://images.unsplash.com/photo-1563089145-599997674d42?w=1920&q=80&auto=format&fit=crop",
  "https://images.unsplash.com/photo-1618005182384-a83a8bd57fbe?w=1920&q=80&auto=format&fit=crop",
  "https://images.unsplash.com/photo-1569701813229-33284b643e3c?w=1920&q=80&auto=format&fit=crop",
  "https://images.unsplash.com/photo-1579783902614-a3fb3927b675?w=1920&q=80&auto=format&fit=crop"
];
let currentWallpaperIdx = 0;
let wallpaperTimer = null;

function initAnimeWallpaperEngine() {
  changeAnimeWallpaper(true);
  // 每 25 秒自动随机渐变切换下一张二次元壁纸
  if (wallpaperTimer) clearInterval(wallpaperTimer);
  wallpaperTimer = setInterval(() => {
    changeAnimeWallpaper(false);
  }, 25000);
}

function changeAnimeWallpaper(isInitial = false) {
  const bgEl = document.getElementById("wallpaper-anime-bg");
  if (!bgEl) return;

  if (isInitial) {
    currentWallpaperIdx = Math.floor(Math.random() * ANIME_WALLPAPERS.length);
  } else {
    currentWallpaperIdx = (currentWallpaperIdx + 1) % ANIME_WALLPAPERS.length;
  }

  const nextUrl = ANIME_WALLPAPERS[currentWallpaperIdx];
  const img = new Image();
  img.src = nextUrl;
  img.onload = () => {
    bgEl.style.opacity = "0.05";
    setTimeout(() => {
      bgEl.style.backgroundImage = `url('${nextUrl}')`;
      bgEl.style.opacity = "0.32";
    }, 300);
  };
}

// 检测移动端或 Android APK 环境
function checkMobileApkEnvironment() {
  const ua = navigator.userAgent.toLowerCase();
  const isAndroid = ua.includes("android");
  const isMobile = /iphone|ipad|ipod|android|mobile/i.test(ua);
  const isApkWebView = window.AndroidNative !== undefined || window.Capacitor !== undefined || ua.includes("wv") || ua.includes("version/");

  // 如果处于 APK 或移动设备环境中，优化登录交互
  const apkBanner = document.getElementById("apk-phone-detect-banner");
  const apkBadge = document.getElementById("apk-detect-badge");
  const apkText = document.getElementById("apk-detect-text");

  if (isApkWebView || isMobile) {
    if (apkBadge) apkBadge.innerText = isApkWebView ? "APK 移动运行环境" : "手机移动端环境";
    if (apkText) apkText.innerText = "已就绪：支持一键读取与三要素持久免密直通宿管工作台。";

    // 读取持久化宿管凭证快速免密直登
    const savedPhone = localStorage.getItem("xgh_dorm_phone");
    const savedName = localStorage.getItem("xgh_dorm_name");
    const savedBldg = localStorage.getItem("xgh_dorm_bldg");
    if (savedPhone && savedName && savedBldg && !state.user) {
      console.log("[APK Auth] 发现已保存的宿管三要素，正在尝试自动直登...");
      request("/auth/dorm-quick-login", {
        method: "POST",
        body: JSON.stringify({ phone: savedPhone, real_name: savedName, building: savedBldg })
      }).then(async res => {
        if (res && res.ok) {
          const data = await res.json();
          state.user = data.user;
          localStorage.setItem("xgh_user", JSON.stringify(data.user));
          showWorkspaceView();
          renderUserSlot();
          switchTab("dorm");
        }
      });
    }
  }
}

// 模拟或调用原生注入的读取 SIM 卡手机号
function detectDevicePhoneSim() {
  const phoneInp = document.getElementById("inp-quick-phone");
  if (!phoneInp) return;

  // 1. 如果有 Android 原生注入对象 (如 AndroidNative.getPhoneNumber())
  if (window.AndroidNative && typeof window.AndroidNative.getPhoneNumber === "function") {
    try {
      const nativePhone = window.AndroidNative.getPhoneNumber();
      if (nativePhone) {
        phoneInp.value = nativePhone;
        toast("已通过 APK 底层接口自动获取本机 SIM 卡号码！", "info");
        return;
      }
    } catch (e) {
      console.warn("Native phone read failed:", e);
    }
  }

  // 2. 检查本地上次成功认证的手机号
  const cachedPhone = localStorage.getItem("xgh_dorm_phone");
  if (cachedPhone) {
    phoneInp.value = cachedPhone;
    toast("已自动填入本机上次核验保存的手机号码：" + cachedPhone, "success");
    return;
  }

  // 3. 填入预置测试宿管号码以方便测试体验
  phoneInp.value = "13800008888";
  const nameInp = document.getElementById("inp-quick-name");
  const bldgInp = document.getElementById("inp-quick-bldg");
  if (nameInp && !nameInp.value) nameInp.value = "测试宿管";
  if (bldgInp && !bldgInp.value) bldgInp.value = "1号楼";
  toast("提示：现代 Android 安全策略下已填入预置宿管直登测试三要素！", "success");
}

// 初始化防御层：确保遮罩与抽屉默认绝对不遮挡交互
function initModalAndDrawerGuards() {
  const mask = document.getElementById("side-drawer-mask");
  if (mask) {
    mask.style.display = "none";
    mask.style.pointerEvents = "none";
    mask.style.visibility = "hidden";
  }
  const drawer = document.getElementById("side-drawer");
  if (drawer) {
    drawer.style.visibility = "hidden";
    drawer.style.pointerEvents = "none";
  }
  const mRec = document.getElementById("modal-recruit-window");
  if (mRec) {
    mRec.style.display = "none";
    mRec.style.pointerEvents = "none";
  }
  const mLog = document.getElementById("modal-login-window");
  if (mLog) {
    mLog.style.display = "none";
    mLog.style.pointerEvents = "none";
  }
}

// 全屏壁纸与工作台无缝切换
function showWallpaperView() {
  const wp = document.getElementById("view-wallpaper-landing");
  const ws = document.getElementById("view-workspace-dashboard");
  if (wp) {
    wp.classList.remove("hidden");
    wp.style.display = "flex";
  }
  if (ws) {
    ws.classList.add("hidden");
    ws.style.display = "none";
  }
  document.body.classList.remove("console-mode");
  document.body.classList.add("overflow-hidden");
}

function showWorkspaceView() {
  const wp = document.getElementById("view-wallpaper-landing");
  const ws = document.getElementById("view-workspace-dashboard");
  if (wp) {
    wp.classList.add("hidden");
    wp.style.display = "none";
  }
  if (ws) {
    ws.classList.remove("hidden");
    ws.style.display = "flex";
  }
  document.body.classList.add("console-mode");
  document.body.classList.remove("overflow-hidden");
}

function returnToWallpaper() {
  showWallpaperView();
}

// 侧滑抽屉控制 (Side Drawer)
function toggleSideDrawer() {
  const drawer = document.getElementById("side-drawer");
  const mask = document.getElementById("side-drawer-mask");
  if (!drawer || !mask) return;

  if (drawer.classList.contains("open")) {
    drawer.classList.remove("open");
    drawer.style.visibility = "hidden";
    drawer.style.pointerEvents = "none";
    mask.classList.add("hidden");
    mask.style.display = "none";
    mask.style.pointerEvents = "none";
    mask.style.visibility = "hidden";
  } else {
    drawer.classList.add("open");
    drawer.style.visibility = "visible";
    drawer.style.pointerEvents = "auto";
    mask.classList.remove("hidden");
    mask.style.display = "block";
    mask.style.pointerEvents = "auto";
    mask.style.visibility = "visible";
  }
}

// 触摸手势侧滑支持 (手机端向左轻扫唤出)
function initTouchSwipe() {
  let touchStartX = 0;
  window.addEventListener('touchstart', e => {
    touchStartX = e.changedTouches[0].screenX;
  }, { passive: true });
  window.addEventListener('touchend', e => {
    const touchEndX = e.changedTouches[0].screenX;
    if (touchStartX - touchEndX > 70) {
      const wp = document.getElementById("view-wallpaper-landing");
      if (wp && !wp.classList.contains("hidden")) {
        toggleSideDrawer();
      }
    }
  }, { passive: true });
}

// 招新弹窗窗口
function openRecruitModal() {
  const m = document.getElementById("modal-recruit-window");
  if (m) {
    m.classList.remove("hidden");
    m.style.display = "flex";
    m.style.pointerEvents = "auto";
    m.style.visibility = "visible";
    loadRecruitModalCards();
  }
}

function closeRecruitModal() {
  const m = document.getElementById("modal-recruit-window");
  if (m) {
    m.classList.add("hidden");
    m.style.display = "none";
    m.style.pointerEvents = "none";
    m.style.visibility = "hidden";
  }
}

// 登录弹窗窗口
function openLoginModal() {
  const m = document.getElementById("modal-login-window");
  if (m) {
    m.classList.remove("hidden");
    m.style.display = "flex";
    m.style.pointerEvents = "auto";
    m.style.visibility = "visible";
  }
}

function closeLoginModal() {
  const m = document.getElementById("modal-login-window");
  if (m) {
    m.classList.add("hidden");
    m.style.display = "none";
    m.style.pointerEvents = "none";
    m.style.visibility = "hidden";
  }
}

// 快速直接开考小测
function openExamDirectly() {
  showWorkspaceView();
  switchTab('exam');
}

// 系统时钟与查寝状态实时监测
function startSystemClock() {
  const clockEl = document.getElementById("system-clock-badge");
  const wpClockEl = document.getElementById("wallpaper-live-time");
  const statusEl = document.getElementById("global-work-status");
  
  const update = () => {
    const now = new Date();
    const timeStr = now.toLocaleTimeString();
    if (clockEl) clockEl.innerText = timeStr;
    if (wpClockEl) wpClockEl.innerText = timeStr;

    const h = now.getHours();
    const isWorkTime = (h >= 12 && h <= 13) || (h >= 18 && h <= 22);
    if (statusEl) {
      if (isWorkTime) {
        statusEl.className = "pill-badge pill-badge-dark";
        statusEl.innerHTML = `<span class="status-dot mr-1"></span> 查寝上工进行时`;
      } else {
        statusEl.className = "pill-badge pill-badge-gray";
        statusEl.innerHTML = `<i class="fa-regular fa-clock mr-1"></i> 常规巡查待命`;
      }
    }
  };
  update();
  setInterval(update, 1000);
}

// 通用 HTTP 请求封装
const TOAST_ICONS = {
  success: "fa-circle-check",
  error: "fa-circle-xmark",
  warning: "fa-triangle-exclamation",
  info: "fa-circle-info",
};

function dismissToast(item) {
  if (!item || !item.parentElement) return;
  item.classList.add("toast-leaving");
  setTimeout(() => item.remove(), 180);
}

// 轻提示。message 一律按纯文本渲染：内容可能直接来自后端错误字段，拼进 HTML 会有 XSS 风险
function toast(message, type = "info", duration) {
  let stack = document.getElementById("toast-stack");
  if (!stack) {
    stack = document.createElement("div");
    stack.id = "toast-stack";
    document.body.appendChild(stack);
  }

  const kind = TOAST_ICONS[type] ? type : "info";
  const item = document.createElement("div");
  item.className = `toast-item toast-${kind}`;
  item.setAttribute("role", kind === "error" ? "alert" : "status");

  const icon = document.createElement("i");
  icon.className = `fa-solid ${TOAST_ICONS[kind]} toast-icon`;

  const text = document.createElement("div");
  text.className = "toast-text";
  text.textContent = message === null || message === undefined ? "" : String(message);

  const close = document.createElement("button");
  close.type = "button";
  close.className = "toast-close";
  close.innerHTML = '<i class="fa-solid fa-xmark"></i>';
  close.addEventListener("click", () => dismissToast(item));

  item.append(icon, text, close);
  stack.appendChild(item);

  while (stack.children.length > 5) dismissToast(stack.firstElementChild);

  const ms = duration === undefined ? (kind === "error" ? 6000 : 3500) : duration;
  if (ms > 0) setTimeout(() => dismissToast(item), ms);

  // 错误与成功回执同步登记到通知中心；warning/info 多为表单校验噪声，不登记
  if (kind === "error" || kind === "success") notifyRecord(message, kind);

  return item;
}

// =============================================================================
// 通知中心 (仅本次会话内存留存，不落库、刷新即清)
// =============================================================================
const NOTIFY_LIMIT = 50;
const notifyState = { items: [], seq: 0 };

function notifyRecord(message, kind) {
  notifyState.items.unshift({
    id: ++notifyState.seq,
    kind,
    message: message === null || message === undefined ? "" : String(message),
    at: new Date(),
    read: false,
  });
  if (notifyState.items.length > NOTIFY_LIMIT) notifyState.items.length = NOTIFY_LIMIT;
  renderNotifications();
}

function unreadCount() {
  let n = 0;
  for (const item of notifyState.items) if (!item.read) n++;
  return n;
}

function formatNotifTime(d) {
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function renderNotifications() {
  const badge = document.getElementById("topbar-bell-badge");
  const list = document.getElementById("notif-list");
  const markAll = document.getElementById("notif-mark-all");

  const unread = unreadCount();
  if (badge) {
    badge.textContent = unread > 99 ? "99+" : String(unread);
    badge.hidden = unread === 0;
  }
  if (markAll) markAll.disabled = unread === 0;
  if (!list) return;

  if (notifyState.items.length === 0) {
    list.replaceChildren(mkNode("div", "notif-empty", "本次会话暂无通知"));
    return;
  }

  const frag = document.createDocumentFragment();
  for (const item of notifyState.items) {
    const row = mkNode("button", `notif-item notif-${item.kind}${item.read ? " is-read" : ""}`);
    row.type = "button";
    const line = mkNode("div", "notif-line");
    line.append(mkNode("span", "notif-msg", item.message));
    row.append(line, mkNode("time", "notif-time", formatNotifTime(item.at)));
    row.addEventListener("click", () => {
      item.read = true;
      renderNotifications();
    });
    frag.append(row);
  }
  list.replaceChildren(frag);
}

function toggleNotifications(force) {
  const wrap = document.getElementById("topbar-bell");
  if (!wrap) return;
  const open = force === undefined ? !wrap.classList.contains("open") : force;
  wrap.classList.toggle("open", open);
}

function markAllNotifications() {
  notifyState.items.forEach((item) => { item.read = true; });
  renderNotifications();
}

document.addEventListener("click", (e) => {
  const wrap = document.getElementById("topbar-bell");
  if (!wrap || !wrap.classList.contains("open")) return;
  // 条目点击会触发重渲染并把被点节点摘出 DOM，此时 contains(e.target) 会误判为外部点击
  const path = typeof e.composedPath === "function" ? e.composedPath() : null;
  if (path ? path.includes(wrap) : wrap.contains(e.target)) return;
  toggleNotifications(false);
});

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") toggleNotifications(false);
});

async function request(endpoint, options = {}) {
  const url = `${API_BASE}${endpoint}`;
  const headers = options.headers || {};
  if (!(options.body instanceof FormData)) {
    headers["Content-Type"] = "application/json";
  }

  try {
    const res = await fetch(url, { ...options, headers, credentials: "same-origin" });
    if (res.status === 401) {
      toast("登录已失效，请重新登录", "error");
      logout();
      return null;
    }
    if (res.status === 403) {
      const err = await res.json();
      toast(err.error || "Casbin 权限拦截：您所在的身份角色无权操作此资源", "error");
      return null;
    }
    return res;
  } catch (err) {
    console.error("API Error:", err);
    toast("网络连接异常，请确保后端服务正在监听 :8080", "error");
    return null;
  }
}

// 导出与打包类下载的统一入口：只靠会话 Cookie 鉴权。
// Web 端在 C1 之后不再持有令牌，早先"先查 state.token、再手工拼 Authorization 头"的写法
// 一是恒判未登录，二是即便放过、一个无效的 Bearer 头也会让中间件优先走令牌分支而返回 401。
// 非 2xx 一律不落成文件——否则 401 的 JSON 会被存成一份"能打开的空 CSV"。
async function downloadAuthedFile(url, filename, failLabel) {
  let res;
  try {
    res = await fetch(url, { credentials: "same-origin" });
  } catch (err) {
    toast(`${failLabel}: 网络连接异常`, "error");
    return false;
  }

  if (res.status === 401) {
    toast("登录已失效，请重新登录后再导出", "error");
    return false;
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    toast(`${failLabel}: ${err.error || "导出服务异常 " + res.status}`, "error");
    return false;
  }

  const blob = await res.blob();
  const a = document.createElement("a");
  const href = window.URL.createObjectURL(blob);
  a.href = href;
  a.download = filename;
  a.click();
  window.URL.revokeObjectURL(href);
  return true;
}

// 选项卡切换 (在业务工作台内各面板切换)
function switchTab(tabId) {
  const panels = ["dorm", "member", "leave", "deductions", "minister", "tech", "export", "students", "welfare", "publicity-gallery", "broadcast-news", "security", "exam", "excellence"];
  panels.forEach(p => {
    const el = document.getElementById(`panel-${p}`);
    if (el) el.classList.add("hidden");
  });

  const activePanel = document.getElementById(`panel-${tabId}`);
  if (activePanel) {
    activePanel.classList.remove("hidden");
    state.currentTab = tabId;
  }

  // 侧边栏高亮项同步
  document.querySelectorAll(".newapi-nav-item").forEach(btn => btn.classList.remove("active"));
  const currentNav = document.getElementById(`sidebar-nav-${tabId}`);
  if (currentNav) currentNav.classList.add("active");

  // 同步顶部微标题
  const titleMap = {
    "dorm": "宿管专属工作台",
    "member": "部员上工与个人台账",
    "leave": "独立请假极速申报中心",
    "deductions": "园区查寝打表与违规录入",
    "minister": "部长排班决策与履职大盘",
    "tech": "技术组控制台与底层运维",
    "export": "综合档案导出中心",
    "students": "学生名册特征识别导入",
    "welfare": "积分商城与 AI 福利站",
    "publicity-gallery": "宣传部 · 插画灵感工坊",
    "broadcast-news": "播音组 · 新闻筛选与广播看板",
    "security": "个人安全设置与登录凭证",
    "exam": "在线素养测评考场",
    "excellence": "文明标兵寝室评选榜"
  };
  const titleEl = document.getElementById("topbar-current-page-title");
  if (titleEl && titleMap[tabId]) {
    titleEl.innerText = titleMap[tabId];
  }

  window.scrollTo({ top: 0, behavior: "smooth" });

  if (tabId === "dorm") loadDormPanel();
  if (tabId === "member") loadMemberPanel();
  if (tabId === "leave") loadMemberPanel(); // 刷新可用排班与请假流水
  if (tabId === "deductions") {
    loadMorningDormReports();
    loadDeductionsTable();
  }
  if (tabId === "minister") loadMinisterPanel();
  if (tabId === "tech") loadTechPanel();
  if (tabId === "export") loadExportTable();
  if (tabId === "students") loadStudentSection();
  if (tabId === "welfare") loadWelfarePanel();
  if (tabId === "publicity-gallery") loadPublicityGallery();
  if (tabId === "broadcast-news") loadBroadcastNewsView();
  if (tabId === "security") loadSecuritySettings();
  if (tabId === "exam") loadExamPanel();
  if (tabId === "excellence") loadRoomExcellenceBoard();
}

// 依据角色重定向到工作台专属入口
function routeUserToDefault() {
  if (!state.user) return;
  switch (state.user.role) {
    case "dorm_manager": switchTab("dorm"); break;
    case "member": switchTab("member"); break;
    case "minister": switchTab("minister"); break;
    case "tech_admin": switchTab("tech"); break;
    case "viewer_export": switchTab("export"); break;
    default: switchTab("member"); break;
  }
}

// 打表授权的前端镜像判定，口径与后端 model.HasDeductionAuthority 一致。
// 只用于决定显示哪个分栏；真正的闸门在服务端 handler，这里判错也越不了权。
function hasDeductionAuthority(user) {
  if (!user || user.status === "disabled") return false;
  if (user.role === "tech_admin") return true;
  return (user.role === "member" || user.role === "minister") &&
    (user.department || "").includes("技术") && user.position === "副部长";
}

// 身份实时对齐：部门与职务会在部长任免的那一刻改变打表权限，localStorage 里的
// state.user 只是缓存。启动时与标签页切回时用 /auth/profile 覆盖一次，
// 权限被回收的人不必重新登录，也不会继续停在已失效的打表面板上。
let lastIdentitySyncAt = 0;

async function syncIdentityFromServer(options = {}) {
  if (!state.user) return;
  if (!options.force && Date.now() - lastIdentitySyncAt < 60000) return;

  const res = await request("/auth/profile", { method: "GET" });
  if (!res || !res.ok) return;
  const fresh = await res.json();
  if (!fresh || !fresh.role) return;
  lastIdentitySyncAt = Date.now();

  if (fresh.status === "disabled") {
    toast("该账号已被技术维护组停用，请重新登录", "warning");
    logout();
    return;
  }

  const previous = state.user;
  const labels = { role: "角色", department: "部门", position: "职务", building: "楼栋", floor: "楼层", real_name: "姓名" };
  const changed = Object.keys(labels).filter(k => (previous[k] || "") !== (fresh[k] || ""));
  state.user = fresh;
  localStorage.setItem("xgh_user", JSON.stringify(fresh));
  if (!changed.length) return;

  renderUserSlot();
  toast(`账号信息已同步：${changed.map(k => `${labels[k]}→${fresh[k] || "未设置"}`).join("，")}`, "info");

  if (state.currentTab === "deductions" && !hasDeductionAuthority(fresh)) {
    switchTab(fresh.role === "minister" ? "minister" : "member");
  }
}

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") syncIdentityFromServer();
});

// NewAPI 风格左侧垂直固定侧边栏与用户信息卡片渲染
function mkNode(tag, className, text) {
  const n = document.createElement(tag);
  if (className) n.className = className;
  if (text !== undefined && text !== null) n.textContent = String(text);
  return n;
}

// 顶栏用户胶囊：常态只显示头像与昵称，详情由 CSS 在悬停/聚焦时展开
function renderTopbarUser() {
  const wrap = document.getElementById("topbar-user");
  const avatar = document.getElementById("topbar-user-avatar");
  const name = document.getElementById("topbar-user-name");
  const pop = document.getElementById("topbar-user-pop");
  if (!wrap || !avatar || !name || !pop) return;

  const user = state.user;
  if (!user) {
    wrap.hidden = true;
    pop.replaceChildren();
    return;
  }

  const initial = (user.real_name || "?").charAt(0);
  wrap.hidden = false;
  avatar.textContent = initial;
  name.textContent = user.real_name || "未命名";

  const head = mkNode("div", "user-pop-head");
  const headText = mkNode("div");
  headText.append(
    mkNode("div", "user-pop-name", user.real_name || "未命名"),
    mkNode("div", "user-pop-sub", [user.department, user.position].filter(Boolean).join(" · ") || "学管会")
  );
  head.append(mkNode("div", "user-pop-avatar", initial), headText);

  const rows = mkNode("dl", "user-pop-rows");
  const addRow = (label, value) => {
    if (value === undefined || value === null || value === "") return;
    const row = mkNode("div", "user-pop-row");
    row.append(mkNode("dt", null, label), mkNode("dd", null, value));
    rows.append(row);
  };
  addRow("登录账号", user.username);
  addRow("身份角色", user.role);
  addRow("所属楼栋", user.building);
  addRow("负责楼层", user.floor);
  addRow("所在班级", user.class_name);
  addRow("联系电话", user.phone);
  addRow("履职积分", `${user.total_score ?? 100} 分`);

  const actions = mkNode("div", "user-pop-actions");
  const secBtn = mkNode("button", "btn-pill btn-pill-light", "安全设置");
  secBtn.type = "button";
  secBtn.addEventListener("click", () => switchTab("security"));
  const outBtn = mkNode("button", "btn-pill btn-pill-light", "退出登录");
  outBtn.type = "button";
  outBtn.addEventListener("click", () => logout());
  actions.append(secBtn, outBtn);

  pop.replaceChildren(head, rows, actions);
}

function renderUserSlot() {
  const navContainer = document.getElementById("sidebar-nav-container");
  if (!navContainer) return;

  renderTopbarUser();

  if (state.user) {

    // 2. 根据身份角色严格组织定制左侧分组菜单 (NewAPI 分组导航规范)
    let navHtml = "";

    const isTechViceMinister = hasDeductionAuthority(state.user);

    // 针对普通部员 (member)
    if (state.user.role === "member") {
      navHtml += `
        <div class="sidebar-category-label">部员服务</div>
        <button onclick="switchTab('member')" id="sidebar-nav-member" class="newapi-nav-item w-full">
          <i class="fa-solid fa-chart-line text-zinc-500"></i>
          <span>上工数据浏览</span>
        </button>
        <button onclick="switchTab('leave')" id="sidebar-nav-leave" class="newapi-nav-item w-full">
          <i class="fa-solid fa-paper-plane text-zinc-500"></i>
          <span>快速请假申报</span>
        </button>
        <button onclick="switchTab('welfare')" id="sidebar-nav-welfare" class="newapi-nav-item w-full">
          <i class="fa-solid fa-gift text-amber-500"></i>
          <span>积分商城 (${state.user.department || '本部'}部长定制)</span>
        </button>
        <button onclick="switchTab('exam')" class="newapi-nav-item w-full">
          <i class="fa-solid fa-pen-to-square text-sky-500"></i>
          <span>在线素养测评考场</span>
        </button>
      `;

      // 仅当是【技术部副部长】时，专属开通【数据总结与宿管上午数据打表分栏】！其他纪检等部员不打表、不展示打表
      if (isTechViceMinister) {
        navHtml += `
          <div class="sidebar-category-label">技术部副部长权限</div>
          <button onclick="switchTab('deductions')" id="sidebar-nav-deductions" class="newapi-nav-item w-full">
            <i class="fa-solid fa-table-list text-amber-500"></i>
            <span>宿管上午数据打表汇总</span>
          </button>
        `;
      }

      // 宣传部部员：专属插画灵感与播音
      if ((state.user.department || "").includes("宣传部")) {
        navHtml += `
          <div class="sidebar-category-label">宣传专属</div>
          <button onclick="switchTab('publicity-gallery')" id="sidebar-nav-publicity-gallery" class="newapi-nav-item w-full">
            <i class="fa-solid fa-palette text-fuchsia-500"></i>
            <span>插画随机灵感素材</span>
          </button>
          <button onclick="switchTab('broadcast-news')" id="sidebar-nav-broadcast-news" class="newapi-nav-item w-full">
            <i class="fa-solid fa-microphone-lines text-sky-500"></i>
            <span>新闻筛选与广播看板</span>
          </button>
        `;
      }
    }

    // 针对部长 (minister)
    if (state.user.role === "minister") {
      navHtml += `
        <div class="sidebar-category-label">部长决策</div>
        <button onclick="switchTab('member')" id="sidebar-nav-member" class="newapi-nav-item w-full">
          <i class="fa-solid fa-user text-zinc-500"></i>
          <span>本人上工数据</span>
        </button>
        <button onclick="switchTab('minister'); switchMinisterSubTab('overview');" id="sidebar-nav-minister" class="newapi-nav-item w-full">
          <i class="fa-solid fa-users text-zinc-500"></i>
          <span>部员上工与积分台账</span>
        </button>
        <button onclick="switchTab('minister'); switchMinisterSubTab('members-mgr');" class="newapi-nav-item w-full">
          <i class="fa-solid fa-user-gear text-indigo-500"></i>
          <span>管理部员 / 任命副部长</span>
        </button>
        <button onclick="switchTab('minister'); switchMinisterSubTab('overview'); scrollToWeekDutySpectrum();" class="newapi-nav-item w-full">
          <i class="fa-solid fa-traffic-light text-emerald-600"></i>
          <span>部员排班表 (三色履职)</span>
        </button>
        <button onclick="switchTab('minister'); switchMinisterSubTab('ai-schedule');" class="newapi-nav-item w-full">
          <i class="fa-solid fa-wand-magic-sparkles text-amber-500"></i>
          <span>AI 对话智能排表</span>
        </button>
        <button onclick="switchTab('minister'); switchMinisterSubTab('overview'); scrollToMinisterLeaveReview();" class="newapi-nav-item w-full">
          <i class="fa-solid fa-check-to-slot text-zinc-500"></i>
          <span>部员请假审批</span>
        </button>
        <button onclick="switchTab('leave')" id="sidebar-nav-leave" class="newapi-nav-item w-full">
          <i class="fa-solid fa-paper-plane text-zinc-500"></i>
          <span>部长请假备案</span>
        </button>
        <button onclick="switchTab('welfare')" id="sidebar-nav-welfare" class="newapi-nav-item w-full">
          <i class="fa-solid fa-sliders text-amber-500"></i>
          <span>设置本部福利/模型价格</span>
        </button>
        <button onclick="switchTab('minister'); switchMinisterSubTab('recruit');" class="newapi-nav-item w-full">
          <i class="fa-solid fa-user-plus text-emerald-600"></i>
          <span>招新报名审核</span>
        </button>
        <button onclick="switchTab('excellence')" class="newapi-nav-item w-full">
          <i class="fa-solid fa-award text-amber-500"></i>
          <span>文明标兵寝室评选榜</span>
        </button>
        <button onclick="switchTab('exam')" class="newapi-nav-item w-full">
          <i class="fa-solid fa-pen-to-square text-sky-500"></i>
          <span>在线素养测评考场</span>
        </button>
      `;

      // 宣传部部长：插画灵感与播音
      if ((state.user.department || "").includes("宣传部")) {
        navHtml += `
          <div class="sidebar-category-label">宣传视界</div>
          <button onclick="switchTab('publicity-gallery')" id="sidebar-nav-publicity-gallery" class="newapi-nav-item w-full">
            <i class="fa-solid fa-palette text-fuchsia-500"></i>
            <span>插画灵感 API 获取</span>
          </button>
          <button onclick="switchTab('broadcast-news')" id="sidebar-nav-broadcast-news" class="newapi-nav-item w-full">
            <i class="fa-solid fa-microphone-lines text-sky-500"></i>
            <span>播音新闻与标兵推送</span>
          </button>
        `;
      }

      // 部长本人若同时是技术部门副部长，服务端同样放行打表，分栏不能缺席
      if (isTechViceMinister) {
        navHtml += `
          <div class="sidebar-category-label">技术部副部长权限</div>
          <button onclick="switchTab('deductions')" id="sidebar-nav-deductions" class="newapi-nav-item w-full">
            <i class="fa-solid fa-table-list text-amber-500"></i>
            <span>宿管上午数据打表汇总</span>
          </button>
        `;
      }
    }

    // 针对技术维护组 / 技术部长 (tech_admin)
    if (state.user.role === "tech_admin") {
      navHtml += `
        <div class="sidebar-category-label">部长决策与排班</div>
        <button onclick="switchTab('minister'); switchMinisterSubTab('overview');" id="sidebar-nav-minister" class="newapi-nav-item w-full">
          <i class="fa-solid fa-traffic-light text-emerald-600"></i>
          <span>部员排班表 (三色履职)</span>
        </button>
        <button onclick="switchTab('minister'); switchMinisterSubTab('members-mgr');" class="newapi-nav-item w-full">
          <i class="fa-solid fa-user-gear text-indigo-500"></i>
          <span>管理部员 / 任命副部长</span>
        </button>
        <button onclick="switchTab('minister'); switchMinisterSubTab('ai-schedule');" class="newapi-nav-item w-full">
          <i class="fa-solid fa-wand-magic-sparkles text-amber-500"></i>
          <span>AI 对话排表中枢</span>
        </button>
        <button onclick="switchTab('minister'); switchMinisterSubTab('recruit');" class="newapi-nav-item w-full">
          <i class="fa-solid fa-user-plus text-emerald-600"></i>
          <span>招新报名审核</span>
        </button>
        <button onclick="switchTab('excellence')" class="newapi-nav-item w-full">
          <i class="fa-solid fa-award text-amber-500"></i>
          <span>文明标兵寝室评选榜</span>
        </button>
        <button onclick="switchTab('leave')" id="sidebar-nav-leave" class="newapi-nav-item w-full">
          <i class="fa-solid fa-paper-plane text-zinc-500"></i>
          <span>请假申报中枢</span>
        </button>
        <button onclick="switchTab('deductions')" id="sidebar-nav-deductions" class="newapi-nav-item w-full">
          <i class="fa-solid fa-table-list text-amber-500"></i>
          <span>宿管上午数据打表汇总</span>
        </button>
        <button onclick="switchTab('welfare')" id="sidebar-nav-welfare" class="newapi-nav-item w-full">
          <i class="fa-solid fa-sliders text-amber-500"></i>
          <span>模型定价与福利网关</span>
        </button>

        <div class="sidebar-category-label">技术数据库与底层</div>
        <button onclick="switchTab('tech'); switchTechSubTab('db');" id="sidebar-nav-tech" class="newapi-nav-item w-full">
          <i class="fa-solid fa-database text-zinc-700"></i>
          <span>后台数据库管理 (8核心表)</span>
        </button>
        <button onclick="switchTab('students')" id="sidebar-nav-students" class="newapi-nav-item w-full">
          <i class="fa-solid fa-users-viewfinder text-zinc-700"></i>
          <span>学生名册智能导入中台</span>
        </button>
        <button onclick="switchTab('tech'); switchTechSubTab('slots');" class="newapi-nav-item w-full">
          <i class="fa-solid fa-clock text-zinc-700"></i>
          <span>宿管时段提交规范配置</span>
        </button>
        <button onclick="switchTab('tech'); switchTechSubTab('ai');" class="newapi-nav-item w-full">
          <i class="fa-solid fa-id-card text-zinc-700"></i>
          <span>宿管花名册与全员透视</span>
        </button>
        <button onclick="switchTab('export')" id="sidebar-nav-export" class="newapi-nav-item w-full">
          <i class="fa-solid fa-box-archive text-zinc-700"></i>
          <span>综合档案多合一导出</span>
        </button>

        <div class="sidebar-category-label">公共工坊</div>
        <button onclick="switchTab('publicity-gallery')" id="sidebar-nav-publicity-gallery" class="newapi-nav-item w-full">
          <i class="fa-solid fa-palette text-zinc-500"></i>
          <span>插画素材工坊</span>
        </button>
        <button onclick="switchTab('broadcast-news')" id="sidebar-nav-broadcast-news" class="newapi-nav-item w-full">
          <i class="fa-solid fa-newspaper text-zinc-500"></i>
          <span>广播新闻筛选看板</span>
        </button>
        <button onclick="switchTab('dorm')" id="sidebar-nav-dorm" class="newapi-nav-item w-full">
          <i class="fa-solid fa-house-chimney-user text-zinc-500"></i>
          <span>宿管工作台视界</span>
        </button>
      `;
    }

    // 针对宿管 (dorm_manager)
    if (state.user.role === "dorm_manager") {
      navHtml += `
        <div class="sidebar-category-label">宿管工作台</div>
        <button onclick="switchTab('dorm')" id="sidebar-nav-dorm" class="newapi-nav-item w-full active">
          <i class="fa-solid fa-camera text-black"></i>
          <span>拍照留痕与隐患申报</span>
        </button>
      `;
    }

    // 针对信息查看下载组 (viewer_export)
    if (state.user.role === "viewer_export") {
      navHtml += `
        <div class="sidebar-category-label">综合归档</div>
        <button onclick="switchTab('export')" id="sidebar-nav-export" class="newapi-nav-item w-full active">
          <i class="fa-solid fa-file-zipper text-black"></i>
          <span>综合档案一键打包导出</span>
        </button>
        <button onclick="switchTab('students')" id="sidebar-nav-students" class="newapi-nav-item w-full">
          <i class="fa-solid fa-users text-zinc-500"></i>
          <span>全校学生宿位名册</span>
        </button>
        <button onclick="switchTab('excellence')" class="newapi-nav-item w-full">
          <i class="fa-solid fa-award text-amber-500"></i>
          <span>文明标兵寝室评选榜</span>
        </button>
      `;
    }

    // 所有登录角色通用：安全设置页面
    navHtml += `
      <div class="sidebar-category-label">系统账户</div>
      <button onclick="switchTab('security')" id="sidebar-nav-security" class="newapi-nav-item w-full">
        <i class="fa-solid fa-user-gear text-zinc-500"></i>
        <span>安全设置 (修改账号密码)</span>
      </button>
    `;

    navContainer.innerHTML = navHtml;
  }
}

// 辅助快速定位方法
function scrollToMemberLeave() {
  switchTab("leave");
}

function scrollToOrgDeduction() {
  switchTab("deductions");
}

function scrollToWeekDutySpectrum() {
  setTimeout(() => {
    const el = document.getElementById("duty-spectrum-week-badge");
    if (el) el.scrollIntoView({ behavior: "smooth" });
  }, 100);
}

function scrollToMinisterLeaveReview() {
  setTimeout(() => {
    const el = document.getElementById("minister-pending-badge");
    if (el) el.scrollIntoView({ behavior: "smooth" });
  }, 100);
}

// 退出登录并返回壁纸主页
function logout() {
  // 服务端登出：清除会话 Cookie 并写入审计留痕；失败也不阻塞本地退出
  fetch(`${API_BASE}/auth/logout`, { method: "POST", credentials: "same-origin" }).catch(() => {});
  localStorage.removeItem("xgh_user");
  state.user = null;
  renderTopbarUser();
  showWallpaperView();
}

// 登录模式切换
function setLoginMode(mode) {
  state.loginMode = mode;
  const btnPwd = document.getElementById("btn-login-pwd");
  const btnQuick = document.getElementById("btn-login-quick");
  const formPwd = document.getElementById("login-form-pwd");
  const formQuick = document.getElementById("login-form-quick");

  if (mode === "password") {
    btnPwd.className = "nav-pill-item active flex-1 text-center text-xs";
    btnQuick.className = "nav-pill-item flex-1 text-center text-xs";
    formPwd.classList.remove("hidden");
    formQuick.classList.add("hidden");
  } else {
    btnQuick.className = "nav-pill-item active flex-1 text-center text-xs";
    btnPwd.className = "nav-pill-item flex-1 text-center text-xs";
    formQuick.classList.remove("hidden");
    formPwd.classList.add("hidden");
  }
}

function fillDemo(username) {
  setLoginMode("password");
  document.getElementById("inp-login-user").value = username;
  document.getElementById("inp-login-pwd").value = "123456";
}

// 统一账号密码登录
async function handleLoginSubmit(e) {
  e.preventDefault();
  const username = document.getElementById("inp-login-user").value.trim();
  const password = document.getElementById("inp-login-pwd").value.trim();

  const res = await request("/auth/login", {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });

  if (res && res.ok) {
    const data = await res.json();
    // 会话由服务端 HttpOnly Cookie 承载，token 不再入 localStorage（响应中的 token 仅供 APK 使用）
    state.user = data.user;
    localStorage.setItem("xgh_user", JSON.stringify(data.user));
    closeLoginModal();
    showWorkspaceView();
    renderUserSlot();
    routeUserToDefault();
  } else if (res) {
    const err = await res.json();
    toast(err.error || "账号或密码错误", "error");
  }
}

// 宿管手机端三要素免密直登
async function handleQuickLoginSubmit(e) {
  e.preventDefault();
  const phone = document.getElementById("inp-quick-phone").value.trim();
  const name = document.getElementById("inp-quick-name").value.trim();
  const building = document.getElementById("inp-quick-bldg").value.trim();

  const res = await request("/auth/dorm-quick-login", {
    method: "POST",
    body: JSON.stringify({ phone, real_name: name, building }),
  });

  if (res && res.ok) {
    const data = await res.json();
    state.user = data.user;
    localStorage.setItem("xgh_user", JSON.stringify(data.user));

    // 持久化保存宿管认证三要素，供移动 APK 或下次静默免密自动登录
    localStorage.setItem("xgh_dorm_phone", phone);
    localStorage.setItem("xgh_dorm_name", name);
    localStorage.setItem("xgh_dorm_bldg", building);

    toast(data.message || "宿管三要素核验通过，快捷登入！", "info");
    closeLoginModal();
    showWorkspaceView();
    renderUserSlot();
    switchTab("dorm");
  } else if (res) {
    const err = await res.json();
    toast(err.error || "三要素核验失败，请核对手机号、姓名与负责楼栋", "error");
  }
}

// 加载招新弹窗内的部门卡片与大屏实时申报计数
async function loadRecruitModalCards() {
  const res = await request("/recruit/info", { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();

  const counterEl = document.getElementById("rec-live-counter");
  if (counterEl) {
    counterEl.innerText = data.total_applied || 0;
  }

  const grid = document.getElementById("dept-grid-modal");
  if (!grid) return;

  grid.innerHTML = data.departments.map(d => `
    <div class="p-4 rounded-2xl bg-white/5 border border-white/10 space-y-2 text-xs hover:border-white/30 transition">
      <div class="flex items-center justify-between">
        <span class="font-black text-white text-sm">${d.name}</span>
        <span class="pill-badge pill-badge-dark text-[9px] bg-white/10 text-zinc-300 font-mono">${d.badge}</span>
      </div>
      <p class="text-[11px] text-zinc-400 leading-relaxed">${d.desc}</p>
      <div class="pt-1 text-[10px] text-amber-300/90 font-medium">
        <i class="fa-solid fa-sparkles mr-1"></i> ${d.requirement}
      </div>
    </div>
  `).join("");
}

// 班级快捷填充
function fillQuickClass(className) {
  const inp = document.getElementById("rec-quick-class");
  if (inp) {
    inp.value = className;
    inp.focus();
  }
}

// 意向部门卡片选择高亮
function selectRecruitDept(deptName) {
  const hidInp = document.getElementById("rec-quick-selected-dept");
  if (hidInp) hidInp.value = deptName;

  const cardMap = {
    "组织部 · 技术组": "card-dept-tech",
    "组织部 · 督查组": "card-dept-ducha",
    "宣传部 · 播音组": "card-dept-boyin",
    "宣传部 · 宣传组": "card-dept-xc",
    "纪检部": "card-dept-jj",
  };

  Object.entries(cardMap).forEach(([name, id]) => {
    const el = document.getElementById(id);
    if (!el) return;
    if (name === deptName) {
      el.className = "p-3 rounded-2xl border-2 border-white bg-white text-black cursor-pointer transition flex flex-col justify-between space-y-1 shadow-lg scale-105";
      const icon = el.querySelector("i");
      if (icon) icon.className = icon.className.replace(/text-[a-z]+-[0-9]+/g, "") + " text-black";
      const span = el.querySelector("span:last-child");
      if (span) span.className = "text-[10px] text-zinc-600";
    } else {
      el.className = "p-3 rounded-2xl border border-white/20 bg-white/5 text-white hover:border-white/50 cursor-pointer transition flex flex-col justify-between space-y-1";
      const span = el.querySelector("span:last-child");
      if (span) span.className = "text-[10px] text-zinc-400";
    }
  });
}

// 班级多媒体大屏极简一键申报 (仅需班级与姓名)
async function handleQuickRecruitSubmit(e) {
  e.preventDefault();
  const cls = document.getElementById("rec-quick-class").value.trim();
  const name = document.getElementById("rec-quick-name").value.trim();
  const dept = document.getElementById("rec-quick-selected-dept").value;

  if (!cls || !name) {
    toast("请在班级大屏上填写你的班级与姓名！", "warning");
    return;
  }

  const btn = document.getElementById("btn-quick-recruit-submit");
  if (btn) {
    btn.disabled = true;
    btn.innerHTML = `<i class="fa-solid fa-spinner fa-spin mr-2"></i> 正在生成录取流水号入库...`;
  }

  const res = await request("/recruit/apply", {
    method: "POST",
    body: JSON.stringify({
      real_name: name,
      major_and_class: cls,
      target_department: dept,
      phone: "",
      building_room: "",
    }),
  });

  if (btn) {
    btn.disabled = false;
    btn.innerHTML = `<i class="fa-solid fa-bolt text-black text-lg"></i> <span>⚡ 立即一键申报加入学管会</span>`;
  }

  if (res && res.ok) {
    const data = await res.json();
    const succCard = document.getElementById("rec-quick-success-card");
    const succName = document.getElementById("rec-succ-name-display");
    const succMsg = document.getElementById("rec-succ-msg-display");
    const succNo = document.getElementById("rec-succ-no");

    if (succCard) {
      succCard.classList.remove("hidden");
      if (succName) succName.innerText = `🎉 恭喜【${data.real_name}】同学申报成功！`;
      if (succMsg) succMsg.innerText = `志愿【${data.target_department}】已成功入库，欢迎成为学管会新生力量！`;
      if (succNo) succNo.innerText = data.admission_no || `XGH-2026-${data.application_id}`;
      succCard.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }

    const counterEl = document.getElementById("rec-live-counter");
    if (counterEl && data.total_applied) {
      counterEl.innerText = data.total_applied;
    }
  } else if (res) {
    const err = await res.json();
    toast(err.error || "提交异常，请稍后重试", "error");
  }
}

// 欢迎下一位同学继续申报：保留班级，清空姓名
function resetQuickApplyForNextStudent() {
  const succCard = document.getElementById("rec-quick-success-card");
  if (succCard) succCard.classList.add("hidden");

  const nameInp = document.getElementById("rec-quick-name");
  if (nameInp) {
    nameInp.value = "";
    nameInp.focus();
  }
}

// 兼容常规详细投递
async function handleRecruitSubmit(e) {
  e.preventDefault();
  const payload = {
    real_name: document.getElementById("rec-name").value.trim(),
    gender: "男",
    phone: document.getElementById("rec-phone").value.trim(),
    major_and_class: document.getElementById("rec-major").value.trim(),
    building_room: document.getElementById("rec-room").value.trim(),
    target_department: document.getElementById("rec-dept").value,
    self_introduction: document.getElementById("rec-exp").value.trim(),
    experience_skills: document.getElementById("rec-exp").value.trim(),
  };

  const res = await request("/recruit/apply", {
    method: "POST",
    body: JSON.stringify(payload),
  });

  if (res && res.ok) {
    toast("报名表提交成功！学管会已收到您的意向信息。", "success");
    closeRecruitModal();
  }
}

// =============================================================================
// 2. 在线素养测评考场逻辑
// =============================================================================
const EXAM_SCOPE_LABEL = {
  recruit: "招新笔试",
  member_training: "部员培训考核",
  dorm_check: "宿舍检查自测",
  all: "通用卷",
};

async function loadExamPanel() {
  const list = document.getElementById("exam-paper-list");
  if (!list) return;
  list.innerHTML = `<div class="text-center py-8 text-zinc-400 text-xs col-span-full">加载试卷中...</div>`;

  const res = await request("/public/exam/papers", { method: "GET" });
  if (!res || !res.ok) {
    list.innerHTML = `<div class="text-center py-8 text-rose-500 text-xs col-span-full">试卷读取失败，请稍后重试</div>`;
    return;
  }
  const data = await res.json();
  const papers = data.items || [];
  const badge = document.getElementById("exam-paper-count-badge");
  if (badge) badge.innerText = `${papers.length} 份`;

  if (papers.length === 0) {
    list.innerHTML = `<div class="text-center py-8 text-zinc-400 text-xs col-span-full">当前没有已发布的试卷，请先由技术维护组录入题库</div>`;
    return;
  }

  list.innerHTML = papers.map(p => {
    const count = p.question_count === undefined ? "" : `<span class="pill-badge pill-badge-gray text-[10px] ml-1">${p.question_count} 题</span>`;
    return `
      <div class="p-4 rounded-2xl border border-zinc-200 bg-zinc-50/70 hover:border-black transition space-y-2">
        <div class="flex items-start justify-between gap-2">
          <div class="font-bold text-black text-xs leading-snug">${escapeHtml(p.title)}</div>
          <span class="pill-badge pill-badge-gray text-[10px] shrink-0">${escapeHtml(EXAM_SCOPE_LABEL[p.scope] || p.scope || "综合")}</span>
        </div>
        <p class="text-[11px] text-zinc-500 line-clamp-2 min-h-[28px]">${escapeHtml(p.description || "暂无简介")}</p>
        <div class="text-[11px] text-zinc-400 font-mono">满分 ${p.total_score} · 及格 ${p.passing_score} · 限时 ${p.duration_minutes} 分钟 ${count}</div>
        <button onclick="openExamPaper(${p.id})" class="btn-pill btn-pill-dark w-full py-1.5 text-[11px]">进入作答</button>
      </div>
    `;
  }).join("");
}

async function openExamPaper(paperID) {
  const res = await request(`/public/exam/papers/${paperID}`, { method: "GET" });
  if (!res || !res.ok) {
    toast("试卷读取失败", "error");
    return;
  }
  const detail = await res.json();
  state.activeExam = detail;
  renderExamContent(detail);

  const box = document.getElementById("exam-paper-box");
  if (box) box.classList.remove("hidden");
  const report = document.getElementById("exam-score-report");
  if (report) {
    report.classList.add("hidden");
    report.innerHTML = "";
  }
  box.scrollIntoView({ behavior: "smooth", block: "start" });
}

function closeExamPaper() {
  state.activeExam = null;
  const box = document.getElementById("exam-paper-box");
  if (box) box.classList.add("hidden");
}

function renderExamContent(paper) {
  const titleEl = document.getElementById("exam-title-text");
  const container = document.getElementById("exam-questions-container");
  if (!container) return;

  if (titleEl) titleEl.innerText = paper.title;
  const meta = document.getElementById("exam-meta-badge");
  if (meta) meta.innerText = `满分 ${paper.total_score} · 及格 ${paper.passing_score} · 共 ${(paper.questions || []).length} 题`;
  const desc = document.getElementById("exam-description");
  if (desc) desc.innerText = paper.description || "";

  container.innerHTML = paper.questions.map((q, idx) => `
    <div class="p-4 rounded-2xl bg-zinc-50/80 border border-zinc-200/80 space-y-3">
      <div class="font-bold text-xs sm:text-sm text-black">
        ${idx + 1}. ${escapeHtml(q.question_text)}
        <span class="text-[11px] font-normal text-zinc-400 ml-1">(${q.type === 'multi' ? '多选题' : '单选题'} · ${q.score}分)</span>
      </div>
      <div class="space-y-1.5 text-xs">
        ${(q.options || []).map(opt => {
          const key = opt.charAt(0);
          const inpType = q.type === 'multi' ? 'checkbox' : 'radio';
          return `
            <label class="flex items-center space-x-2.5 p-2 rounded-xl bg-white border border-zinc-200 hover:border-black cursor-pointer transition">
              <input type="${inpType}" name="exam_q_${q.id}" value="${escapeAttr(key)}" class="text-black">
              <span class="text-zinc-700">${escapeHtml(opt)}</span>
            </label>
          `;
        }).join("")}
      </div>
    </div>
  `).join("");
}

async function handleExamSubmit(e) {
  e.preventDefault();
  if (!state.activeExam) return;

  const applicantName = document.getElementById("exam-name").value.trim();
  const applicantPhone = document.getElementById("exam-phone").value.trim();

  const answers = {};
  state.activeExam.questions.forEach(q => {
    const inputs = document.querySelectorAll(`input[name="exam_q_${q.id}"]:checked`);
    const selected = Array.from(inputs).map(inp => inp.value).sort().join(",");
    answers[q.id] = selected;
  });

  const res = await request(`/public/exam/papers/${state.activeExam.id}/submit`, {
    method: "POST",
    body: JSON.stringify({
      applicant_name: applicantName,
      applicant_phone: applicantPhone,
      answers,
    }),
  });

  if (res && res.ok) {
    const data = await res.json();
    const box = document.getElementById("exam-score-report");
    box.classList.remove("hidden");
    box.innerHTML = `
      <div class="flex items-center justify-between">
        <h4 class="font-black text-lg text-black">智能阅卷成绩报告</h4>
        <span class="pill-badge ${data.is_passed ? 'pill-badge-green' : 'pill-badge-amber'} font-bold">
          ${data.is_passed ? '合格通过' : '未达标'}
        </span>
      </div>
      <div class="text-3xl font-black font-mono text-black">
        ${data.score} <span class="text-xs font-normal text-zinc-400">/ 满分 ${state.activeExam.total_score} 分 (及格 ${data.passing_score}分)</span>
      </div>
      <p class="text-xs text-zinc-500">试卷作答已自动归入系统档案库，供组织部查验。</p>
    `;
    box.scrollIntoView({ behavior: "smooth" });
  } else {
    toast("交卷失败，请检查网络后重试", "error");
  }
}

// =============================================================================
// 2.1 文明标兵寝室评选榜 (文档 5.2 / 25)
// =============================================================================
async function loadRoomExcellenceBoard() {
  const wrap = document.getElementById("excellence-table-wrap");
  if (!wrap) return;
  wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs"><i class="fa-solid fa-spinner fa-spin mr-1"></i> 正在按寝室聚合扣分数据...</div>`;

  const grade = (document.getElementById("excellence-grade") || {}).value || "";
  const params = new URLSearchParams();
  if (grade.trim()) params.set("grade", grade.trim());
  params.set("limit", "20");

  const res = await request(`/deductions/room-excellence?${params.toString()}`, { method: "GET" });
  if (!res) {
    wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs">未加载榜单：账号权限不足或会话已失效</div>`;
    return;
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    wrap.innerHTML = `<div class="text-center py-12 text-rose-500 text-xs">${escapeHtml(err.error || "榜单读取失败")}</div>`;
    return;
  }

  const data = await res.json();
  const set = (id, val) => { const el = document.getElementById(id); if (el) el.innerText = val; };
  set("excellence-total-records", data.total_linked_records);
  set("excellence-total-points", data.total_linked_points);
  set("excellence-students", data.active_students);
  set("excellence-room-count", (data.items || []).length);

  const note = document.getElementById("excellence-note");
  if (note) {
    if (data.note) {
      note.innerText = data.note;
      note.classList.remove("hidden");
    } else {
      note.classList.add("hidden");
    }
  }

  const rows = data.items || [];
  if (rows.length === 0) {
    wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs">暂无可参与排名的寝室扣分数据</div>`;
    return;
  }

  wrap.innerHTML = `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
        <tr>
          <th class="p-2.5">名次</th>
          <th class="p-2.5">楼栋</th>
          <th class="p-2.5">寝室号</th>
          <th class="p-2.5">累计扣分</th>
          <th class="p-2.5">违纪记录数</th>
          <th class="p-2.5">涉及学生数</th>
          <th class="p-2.5">评优参考档位</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100">
        ${rows.map((r, i) => `
          <tr>
            <td class="p-2.5 font-black font-mono text-black">${i + 1}</td>
            <td class="p-2.5 font-bold">${escapeHtml(r.building)}</td>
            <td class="p-2.5 font-mono">${escapeHtml(r.room_number)}</td>
            <td class="p-2.5 font-mono font-bold ${r.total_deduct <= 0 ? 'text-emerald-600' : 'text-rose-600'}">${r.total_deduct}</td>
            <td class="p-2.5 font-mono">${r.record_count}</td>
            <td class="p-2.5 font-mono">${r.involved_students}</td>
            <td class="p-2.5">${i < 3 ? '<span class="pill-badge pill-badge-green text-[10px]"><i class="fa-solid fa-award mr-0.5"></i>标兵候选</span>' : '<span class="pill-badge pill-badge-gray text-[10px]">待观察</span>'}</td>
          </tr>
        `).join("")}
      </tbody>
    </table>
  `;
}

async function exportRoomExcellenceCSV() {
  const grade = ((document.getElementById("excellence-grade") || {}).value || "").trim();
  const url = `${API_BASE}/deductions/room-excellence-csv${grade ? `?grade=${encodeURIComponent(grade)}` : ""}`;
  const ok = await downloadAuthedFile(
    url,
    `文明标兵寝室评选_${new Date().toISOString().slice(0, 10)}.csv`,
    "导出失败"
  );
  if (ok) toast("评选表已导出，可用于评优会议留痕", "success");
}

// =============================================================================
// 3. 宿管极简工作台逻辑 (角色: dorm_manager)
// =============================================================================
let currentDormSlotNoticeData = null;

async function loadDormPanel() {
  if (!state.user) return;
  document.getElementById("dorm-title-name").innerText = `${state.user.real_name}，值班愉快！`;
  document.getElementById("dorm-building-tag").innerText = state.user.building || "东区7号楼";

  // 1. 顶端动态时段任务与必须提交资料横幅同步
  loadDormSlotNotice();

  // 2. 工作时间推送待办卡片
  const res = await request("/dorm/today-tasks", { method: "GET" });
  if (res && res.ok) {
    const data = await res.json();
    const container = document.getElementById("dorm-task-push-cards");
    container.innerHTML = data.cards.map(c => `
      <div class="glass-card p-5 ${c.is_work_time ? 'glow-active bg-zinc-50' : ''} space-y-2">
        <div class="flex items-center justify-between">
          <span class="pill-badge ${c.is_work_time ? 'pill-badge-dark' : 'pill-badge-gray'} text-[10px]">
            ${c.is_work_time ? '工作时间自动置顶提醒' : '今日预置班次'}
          </span>
          <span class="text-xs font-mono text-zinc-400">${c.period}</span>
        </div>
        <h4 class="font-bold text-sm text-black">${c.title}</h4>
        <p class="text-xs text-zinc-500 leading-relaxed">${c.prompt_text}</p>
        <div class="text-xs text-zinc-600 font-medium">当值部员：${c.duty_members} · 所属楼栋：${c.building}</div>
      </div>
    `).join("");
  }

  // 3. 加载历史留痕照片
  loadDormWaterfall();
}

// 宿管工作台顶端：根据当前时段实时获取待提交资料横幅
async function loadDormSlotNotice() {
  const res = await request("/dorm/slot-notice", { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();
  currentDormSlotNoticeData = data;

  const badgeEl = document.getElementById("dorm-slot-badge-text");
  const nameEl = document.getElementById("dorm-slot-name");
  const rangeEl = document.getElementById("dorm-slot-time-range");
  const matEl = document.getElementById("dorm-slot-required-materials");
  const promptEl = document.getElementById("dorm-slot-action-prompt");
  const statusEl = document.getElementById("dorm-slot-submission-status");
  const bannerEl = document.getElementById("dorm-slot-notice-banner");

  if (data.active_slot) {
    const slot = data.active_slot;
    const cfg = slot.config;

    if (bannerEl) {
      bannerEl.className = "rounded-3xl p-6 sm:p-7 border transition-all duration-300 shadow-md relative overflow-hidden bg-white border-black/80";
    }

    if (badgeEl) badgeEl.innerText = "当前活跃时段任务";
    if (nameEl) nameEl.innerText = cfg.slot_name;
    if (rangeEl) rangeEl.innerText = `${cfg.start_time} ~ ${cfg.end_time} (${slot.remaining_minutes > 0 ? '剩余 ' + slot.remaining_minutes + ' 分钟' : '进行中'})`;
    if (matEl) matEl.innerText = cfg.required_materials;
    if (promptEl) {
      promptEl.innerHTML = `<i class="fa-solid fa-bell text-amber-500"></i> <span class="font-medium">${cfg.action_prompt || '请根据规范及时巡查并拍照存证入库'}</span>`;
    }

    if (statusEl) {
      if (slot.has_submitted) {
        statusEl.className = "px-3 py-1.5 rounded-xl bg-emerald-50 text-emerald-700 border border-emerald-200 text-xs font-bold flex items-center gap-1.5";
        statusEl.innerHTML = `<i class="fa-solid fa-circle-check text-emerald-600"></i> 今日已提交存证 (${slot.today_submitted_count} 张)`;
      } else {
        statusEl.className = "px-3 py-1.5 rounded-xl bg-amber-50 text-amber-800 border border-amber-300 text-xs font-bold flex items-center gap-1.5 animate-pulse";
        statusEl.innerHTML = `<i class="fa-solid fa-triangle-exclamation text-amber-600"></i> 本时段待提交资料 (0 张)`;
      }
    }

    // 默认自动切换表单目标类型
    const selType = document.getElementById("dorm-sel-type");
    if (selType && cfg.target_photo_type) {
      selType.value = cfg.target_photo_type;
    }
  } else if (data.next_slot) {
    const slot = data.next_slot;
    const cfg = slot.config;

    if (bannerEl) {
      bannerEl.className = "rounded-3xl p-6 sm:p-7 border transition-all duration-300 shadow-sm relative overflow-hidden bg-zinc-50 border-zinc-200";
    }

    if (badgeEl) badgeEl.innerText = "下一巡查时段预告";
    if (nameEl) nameEl.innerText = cfg.slot_name;
    if (rangeEl) rangeEl.innerText = `${cfg.start_time} ~ ${cfg.end_time}`;
    if (matEl) matEl.innerText = cfg.required_materials;
    if (promptEl) {
      promptEl.innerHTML = `<i class="fa-regular fa-clock text-zinc-400"></i> <span>提前准备：${cfg.action_prompt || '请按时段规范组织巡查'}</span>`;
    }

    if (statusEl) {
      statusEl.className = "px-3 py-1.5 rounded-xl bg-zinc-100 text-zinc-600 text-xs font-bold flex items-center gap-1.5";
      statusEl.innerHTML = `<i class="fa-regular fa-clock"></i> 未到时段区间`;
    }
  } else {
    if (nameEl) nameEl.innerText = "全天常规巡检进行中";
    if (rangeEl) rangeEl.innerText = "全天";
    if (matEl) matEl.innerText = "宿舍违规电器排查、消防设施巡检、公共卫生督查照片";
  }
}

// 快速对准时段资料上传定位
function focusDormUploadWithSlot() {
  const form = document.getElementById("form-dorm-ai-upload");
  if (form) {
    form.scrollIntoView({ behavior: "smooth", block: "center" });
    const cameraInput = document.getElementById("dorm-camera-input");
    if (cameraInput) {
      // 触发展开相机选择或聚焦房间号输入
      const roomInp = document.getElementById("dorm-inp-room");
      if (roomInp && !roomInp.value) {
        roomInp.focus();
      }
    }
  }
}

// 留痕图片占位图：必须是本地 data URI。原先失败时改指向远程图床，
// 后端与公网同时不可达时会退化成无上限的"失败→换源→再失败"循环。
const DORM_IMG_PLACEHOLDER =
  "data:image/svg+xml;charset=utf-8," +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="200"><rect width="400" height="200" fill="#f4f4f5"/><text x="200" y="105" font-size="13" fill="#a1a1aa" text-anchor="middle" font-family="sans-serif">图片不可用</text></svg>`
  );

// 只兜一次：错误是一次性监听，处理前先把 onerror 置空，杜绝自循环。
function guardBrokenImages(container) {
  if (!container || container.dataset.imgGuarded === "1") return;
  container.dataset.imgGuarded = "1";
  container.addEventListener(
    "error",
    (event) => {
      const img = event.target;
      if (!img || img.tagName !== "IMG") return;
      img.onerror = null;
      img.src = DORM_IMG_PLACEHOLDER;
    },
    true
  );
}

const DORM_REPORT_KIND_LABEL = { photo: "现场实拍", note: "记名纸条", text: "纯文本申报" };
const DORM_PHOTO_TYPE_LABEL = { sanitation: "卫生督查", violation: "违规违纪", duty_supervise: "上工监督" };
const DORM_STATUS_LABEL = { uploaded: "已上传待分析", ai_analyzed: "已出识别结论", converted: "已转入打表", archived: "已归档", manual_corrected: "人工纠正后入库" };
const DORM_SEVERITY_LABEL = { low: "低", medium: "中", high: "高", critical: "紧急" };
const DORM_MATCH_STATUS_LABEL = { matched: "已匹配名册", ambiguous: "重名待核", unmatched: "未匹配名册" };

function dormAiStatusBadge(aiStatus) {
  if (aiStatus === "real") return `<span class="pill-badge pill-badge-green text-[9px]"><i class="fa-solid fa-microchip mr-1"></i>AI 真实识别</span>`;
  if (aiStatus === "disabled") return `<span class="pill-badge pill-badge-amber text-[9px]"><i class="fa-solid fa-plug-circle-xmark mr-1"></i>未启用 AI</span>`;
  if (aiStatus === "failed") return `<span class="pill-badge pill-badge-amber text-[9px]"><i class="fa-solid fa-triangle-exclamation mr-1"></i>AI 调用失败</span>`;
  return `<span class="pill-badge pill-badge-gray text-[9px]"><i class="fa-solid fa-circle-question mr-1"></i>识别状态未知（历史数据）</span>`;
}

async function loadDormWaterfall() {
  const res = await request("/dorm/inspections", { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();

  const countEl = document.getElementById("dorm-history-count");
  if (countEl) countEl.innerText = `共 ${Number(data.total) || 0} 条留痕`;
  const container = document.getElementById("dorm-photo-waterfall");
  if (!container) return;
  guardBrokenImages(container);

  const items = Array.isArray(data.items) ? data.items : [];
  if (!items.length) {
    container.innerHTML = `<div class="col-span-full text-center text-zinc-400 text-xs py-8">该楼栋暂无拍照留痕记录</div>`;
    return;
  }

  container.innerHTML = items.map((item) => {
    const id = Number(item.id);
    const text = item.vision_ai_output || item.note_text || "";
    return `
    <div class="glass-card overflow-hidden cursor-pointer hover:border-black transition" role="button" tabindex="0"
         aria-label="查看第 ${id} 条留痕详情"
         onclick="openInspectionDetail(${id})"
         onkeydown="if(event.key==='Enter'||event.key===' '){event.preventDefault();openInspectionDetail(${id})}">
      <div class="h-36 bg-zinc-100 relative">
        <img src="${escapeAttr(item.image_url || DORM_IMG_PLACEHOLDER)}" alt="留痕照片" loading="lazy" decoding="async" class="w-full h-full object-cover">
        <span class="absolute top-2 left-2 pill-badge pill-badge-dark text-[10px]">${escapeHtml(item.category || "未归类")}</span>
        <span class="absolute top-2 right-2 pill-badge pill-badge-gray text-[10px]">扣 ${Number(item.deduct_points) || 0} 分</span>
      </div>
      <div class="p-4 space-y-1 text-xs">
        <div class="flex justify-between items-center font-bold text-black">
          <span>${escapeHtml(item.building)} ${escapeHtml(item.room_number || "")}</span>
          <span class="font-mono text-zinc-400 text-[10px]">${escapeHtml((item.created_at || "").slice(0, 10))}</span>
        </div>
        <div class="flex items-center gap-1.5">
          ${dormAiStatusBadge(item.ai_status)}
          <span class="text-[10px] text-zinc-400">${escapeHtml(DORM_REPORT_KIND_LABEL[item.report_kind] || "现场实拍")}</span>
        </div>
        <p class="text-zinc-500 line-clamp-3 leading-relaxed text-[11px]">${escapeHtml(text || "无识别文本")}</p>
      </div>
    </div>`;
  }).join("");
}

async function openInspectionDetail(id) {
  const numId = Number(id);
  if (!Number.isInteger(numId) || numId <= 0) {
    toast("留痕编号无效", "error");
    return;
  }
  const res = await request(`/dorm/inspections/${numId}`, { method: "GET" });
  if (!res) return; // 401/403 已由 request 统一提示
  if (!res.ok) {
    const err = await res.json().catch(() => null);
    toast((err && err.error) || "读取该条留痕详情失败", "error");
    return;
  }
  const data = await res.json().catch(() => null);
  if (!data || !data.record) {
    toast("该条留痕详情为空", "error");
    return;
  }
  renderInspectionDetail(data);
  document.getElementById("modal-inspection-detail").classList.remove("hidden");
}

function closeInspectionDetailModal() {
  document.getElementById("modal-inspection-detail").classList.add("hidden");
}

function detailRow(label, value) {
  return `<div class="flex items-start justify-between gap-3 py-1 border-b border-zinc-100 last:border-0">
    <span class="text-zinc-400 shrink-0">${escapeHtml(label)}</span>
    <span class="text-black font-bold text-right break-all">${value}</span>
  </div>`;
}

function renderInspectionDetail(data) {
  const r = data.record || {};
  const body = document.getElementById("inspection-detail-body");
  guardBrokenImages(body);

  const structured = data.structured || null;
  const subjects = Array.isArray(data.subjects) ? data.subjects : [];
  const linked = Array.isArray(data.linked_deductions) ? data.linked_deductions : [];
  const createdAt = String(r.created_at || "").replace("T", " ").slice(0, 19);
  const processedAt = String(r.processed_at || "").replace("T", " ").slice(0, 19);

  const subjectRows = subjects.length
    ? subjects.map((s) => `
        <tr>
          <td class="py-1.5 pr-2 font-bold text-black">${escapeHtml(s.raw_name)}</td>
          <td class="py-1.5 pr-2 text-zinc-500">${escapeHtml(s.class_name || "—")}</td>
          <td class="py-1.5 pr-2"><span class="pill-badge ${s.match_status === "matched" ? "pill-badge-green" : "pill-badge-amber"} text-[9px]">${escapeHtml(DORM_MATCH_STATUS_LABEL[s.match_status] || "状态未知")}</span></td>
          <td class="py-1.5 text-zinc-500">${Number(s.converted_deduction_id) > 0 ? `已转打表 #${Number(s.converted_deduction_id)}` : "未转打表"}</td>
        </tr>`).join("")
    : `<tr><td colspan="4" class="py-2 text-zinc-400 text-center">本条上报未记名学生</td></tr>`;

  const linkedRows = linked.length
    ? linked.map((d) => `
        <tr>
          <td class="py-1.5 pr-2 font-bold text-black">${escapeHtml(d.student_name)}</td>
          <td class="py-1.5 pr-2 text-zinc-500">${escapeHtml(d.class_name || "—")}</td>
          <td class="py-1.5 pr-2 text-zinc-600">${escapeHtml(d.category || "—")}</td>
          <td class="py-1.5 pr-2 font-mono text-red-600">-${Number(d.deduct_points) || 0}</td>
          <td class="py-1.5"><span class="pill-badge ${d.status === "revoked" ? "pill-badge-gray" : "pill-badge-dark"} text-[9px]">${d.status === "revoked" ? "已撤销" : "已确认"}</span></td>
        </tr>`).join("")
    : `<tr><td colspan="5" class="py-2 text-zinc-400 text-center">本条留痕尚未转入打表</td></tr>`;

  body.innerHTML = `
    <div class="w-full rounded-2xl overflow-hidden bg-zinc-100 border border-zinc-200 relative">
      <img src="${escapeAttr(r.image_url || DORM_IMG_PLACEHOLDER)}" alt="留痕原图" decoding="async" class="w-full max-h-[42vh] object-contain">
      ${r.image_url ? `<a href="${escapeAttr(r.image_url)}" target="_blank" rel="noopener" class="absolute bottom-2 right-2 pill-badge pill-badge-dark text-[10px] no-underline"><i class="fa-solid fa-up-right-from-square mr-1"></i>查看原图</a>` : ""}
    </div>

    <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-6">
      <div>
        ${detailRow("楼栋寝室", `<span>${escapeHtml(r.building || "—")} ${escapeHtml(r.room_number || "")}</span>`)}
        ${detailRow("上报通道", `<span>${escapeHtml(DORM_REPORT_KIND_LABEL[r.report_kind] || "现场实拍")} · ${escapeHtml(DORM_PHOTO_TYPE_LABEL[r.photo_type] || "其他")}</span>`)}
        ${detailRow("类别 / 严重度", `<span>${escapeHtml(r.category || "未归类")} · ${escapeHtml(DORM_SEVERITY_LABEL[r.severity] || r.severity || "未评")}</span>`)}
        ${detailRow("建议扣分", `<span class="font-mono text-red-600">-${Number(r.deduct_points) || 0}</span>`)}
      </div>
      <div>
        ${detailRow("流转状态", `<span>${escapeHtml(DORM_STATUS_LABEL[r.status] || r.status || "—")}</span>`)}
        ${detailRow("AI 识别", dormAiStatusBadge(r.ai_status))}
        ${detailRow("上报人", `<span>${escapeHtml(r.manager_name || "—")}</span>`)}
        ${detailRow("上报时间", `<span class="font-mono text-[10px]">${escapeHtml(createdAt || "—")}${processedAt ? ` / 处理 ${escapeHtml(processedAt)}` : ""}</span>`)}
      </div>
    </div>

    <div class="space-y-1.5">
      <h5 class="font-extrabold text-black text-[11px]"><i class="fa-regular fa-file-lines mr-1.5"></i>识别原文</h5>
      <p class="text-zinc-600 leading-relaxed whitespace-pre-wrap bg-zinc-50 border border-zinc-200 rounded-2xl p-3 max-h-40 overflow-y-auto">${escapeHtml(r.vision_ai_output || "无识别文本")}</p>
    </div>

    ${r.report_kind && r.report_kind !== "photo" && r.note_text ? `
    <div class="space-y-1.5">
      <h5 class="font-extrabold text-black text-[11px]"><i class="fa-solid fa-pen-ruler mr-1.5"></i>申报原文</h5>
      <p class="text-zinc-600 leading-relaxed whitespace-pre-wrap bg-zinc-50 border border-zinc-200 rounded-2xl p-3 max-h-32 overflow-y-auto">${escapeHtml(r.note_text)}</p>
    </div>` : ""}

    ${structured ? `
    <div class="space-y-1.5">
      <h5 class="font-extrabold text-black text-[11px]"><i class="fa-solid fa-wand-magic-sparkles mr-1.5"></i>结构化归纳</h5>
      <div class="bg-zinc-50 border border-zinc-200 rounded-2xl p-3 space-y-1">
        ${detailRow("摘要", `<span class="font-normal text-zinc-600">${escapeHtml(structured.summary || "—")}</span>`)}
        ${detailRow("处置建议", `<span class="font-normal text-zinc-600">${escapeHtml(structured.action_advice || "—")}</span>`)}
      </div>
    </div>` : `<div class="space-y-1.5">
      <h5 class="font-extrabold text-black text-[11px]"><i class="fa-solid fa-wand-magic-sparkles mr-1.5"></i>结构化归纳</h5>
      <p class="text-zinc-400 bg-zinc-50 border border-zinc-200 rounded-2xl p-3">本条无结构化归纳结论</p>
    </div>`}

    <div class="space-y-1.5">
      <h5 class="font-extrabold text-black text-[11px]">
        <i class="fa-solid fa-list-check mr-1.5"></i>所记名单（${Number(data.subject_total) || subjects.length} 人）
      </h5>
      <div class="border border-zinc-200 rounded-2xl overflow-hidden">
        <table class="w-full text-[11px]">
          <thead class="bg-zinc-50 text-zinc-400 text-left">
            <tr><th class="py-1.5 pr-2 font-bold">姓名</th><th class="py-1.5 pr-2 font-bold">班级</th><th class="py-1.5 pr-2 font-bold">匹配</th><th class="py-1.5 font-bold">打表</th></tr>
          </thead>
          <tbody class="divide-y divide-zinc-100 px-3">${subjectRows}</tbody>
        </table>
      </div>
    </div>

    <div class="space-y-1.5">
      <h5 class="font-extrabold text-black text-[11px]"><i class="fa-solid fa-scale-balanced mr-1.5"></i>已关联的打表扣分（${linked.length} 条）</h5>
      <div class="border border-zinc-200 rounded-2xl overflow-hidden">
        <table class="w-full text-[11px]">
          <thead class="bg-zinc-50 text-zinc-400 text-left">
            <tr><th class="py-1.5 pr-2 font-bold">学生</th><th class="py-1.5 pr-2 font-bold">班级</th><th class="py-1.5 pr-2 font-bold">类别</th><th class="py-1.5 pr-2 font-bold">分值</th><th class="py-1.5 font-bold">状态</th></tr>
          </thead>
          <tbody class="divide-y divide-zinc-100 px-3">${linkedRows}</tbody>
        </table>
      </div>
    </div>

    ${r.review_note ? `
    <div class="space-y-1.5">
      <h5 class="font-extrabold text-black text-[11px]"><i class="fa-solid fa-clock-rotate-left mr-1.5"></i>人工纠正留痕</h5>
      <p class="text-zinc-600 leading-relaxed whitespace-pre-wrap bg-zinc-50 border border-zinc-200 rounded-2xl p-3 max-h-32 overflow-y-auto">${escapeHtml(r.review_note)}</p>
    </div>` : ""}
  `;

  document.getElementById("inspection-detail-title").innerText = `留痕 #${Number(r.id) || ""} · ${r.building || ""} ${r.room_number || ""}`;
}

function handlePreviewImage(event) {
  const file = event.target.files[0];
  if (file) {
    const r = new FileReader();
    r.onload = e => {
      document.getElementById("camera-preview-img").src = e.target.result;
      document.getElementById("camera-preview-box").classList.remove("hidden");
      document.getElementById("camera-prompt").classList.add("hidden");
    };
    r.readAsDataURL(file);
  }
}

async function handleDormUpload(e) {
  e.preventDefault();
  const room = document.getElementById("dorm-inp-room").value.trim();
  const photoType = document.getElementById("dorm-sel-type").value;
  const fileInput = document.getElementById("dorm-camera-input");
  const kindSel = document.getElementById("dorm-sel-kind");
  const reportKind = kindSel ? kindSel.value : "photo";
  const subjects = (document.getElementById("dorm-inp-subjects")?.value || "").trim();

  if (reportKind !== "text" && fileInput.files.length === 0) {
    toast("实拍与记名纸条都必须附现场原图；确实无法拍照时请改选“纯文本名单”。", "warning");
    return;
  }
  if (reportKind === "text" && subjects === "") {
    toast("纯文本上报需要填写被记名学生名单。", "warning");
    return;
  }

  const formData = new FormData();
  formData.append("room_number", room);
  formData.append("photo_type", photoType);
  formData.append("report_kind", reportKind);
  formData.append("subject_names", subjects);
  formData.append("note_text", subjects);
  formData.append("building", state.user ? state.user.building : "东区7号楼");
  if (fileInput.files.length > 0) {
    formData.append("image", fileInput.files[0]);
  }

  const btn = document.getElementById("btn-dorm-upload-submit");
  btn.disabled = true;
  btn.innerHTML = `<i class="fa-solid fa-spinner animate-spin"></i> <span>现场留痕上传与名册核对中...</span>`;

  const res = await request("/dorm/upload-photo", {
    method: "POST",
    body: formData,
  });

  btn.disabled = false;
  btn.innerHTML = `
    <span class="flex items-center gap-2"><i class="fa-solid fa-microchip"></i> 立即上传触发双 AI 智能归类</span>
    <span class="text-xs font-normal opacity-70">多模态识别翻译 + 文本结构化归纳入库</span>
  `;

  if (!res || !res.ok) {
    if (res) {
      const err = await res.json().catch(() => ({}));
      toast(err.error || "上报失败，请检查网络", "error");
    }
    return;
  }

  const data = await res.json();
  let msg = data.message || "上报已留痕入库。";
  if (data.subject_total) {
    msg += `\n名单核对：共 ${data.subject_total} 人，与名册匹配 ${data.subject_matched} 人，查无此人 ${data.subject_unmatched} 人。`;
    if (data.subject_unmatched > 0) {
      toast(msg + "\n未匹配的学生会被标出，需先补录名册或改用防冒名核对后再打表。", "warning", 8000);
    } else {
      toast(msg, "success");
    }
  } else {
    toast(msg, "info");
  }

  const outPanel = document.getElementById("dorm-ai-output-panel");
  outPanel.classList.remove("hidden");
  document.getElementById("dorm-ai-analysis-text").innerText =
    data.vision_analysis || "（无：AI 未完成识别，本条上报不含自动提取内容）";
  document.getElementById("dorm-ai-json-raw").innerText = data.structured_result
    ? JSON.stringify(data.structured_result, null, 2)
    : "（未生成结构化结论：AI 引擎未配置或调用失败，请等待技术部副部长人工看图核对）";

  openDormCorrectionForm(data);

  const subjectsInp = document.getElementById("dorm-inp-subjects");
  if (subjectsInp) subjectsInp.value = "";
  loadDormWaterfall();
}

// -----------------------------------------------------------------------------
// 宿管端：AI 识别结论的人工纠正（纠正结果即最终入库结论）
// -----------------------------------------------------------------------------
let currentDormInspection = null;

function dormFixValue(id) {
  const el = document.getElementById(id);
  return el ? el.value : "";
}

function openDormCorrectionForm(data) {
  const box = document.getElementById("dorm-fix-box");
  if (!box) return;

  const recordId = data.record && data.record.id;
  if (!recordId) {
    box.classList.add("hidden");
    currentDormInspection = null;
    return;
  }

  const s = data.structured_result || {};
  currentDormInspection = {
    id: recordId,
    baseline: {
      vision_analysis: data.vision_analysis || "",
      category: s.category || "",
      severity: s.severity || "medium",
      deduct_points: s.deduct_points === undefined ? 0 : s.deduct_points,
      summary: s.summary || "",
      action_advice: s.action_advice || "",
    },
  };

  document.getElementById("dorm-fix-vision").value = currentDormInspection.baseline.vision_analysis;
  document.getElementById("dorm-fix-category").value = currentDormInspection.baseline.category;
  document.getElementById("dorm-fix-severity").value = currentDormInspection.baseline.severity;
  document.getElementById("dorm-fix-points").value = currentDormInspection.baseline.deduct_points;
  document.getElementById("dorm-fix-summary").value = currentDormInspection.baseline.summary;
  document.getElementById("dorm-fix-advice").value = currentDormInspection.baseline.action_advice;
  document.getElementById("dorm-fix-reason").value = "";

  const hint = document.getElementById("dorm-fix-hint");
  if (hint) {
    hint.innerText = s.category
      ? "识别结论有误就直接改，只提交你改动过的字段。"
      : "本条没有 AI 结构化结论，可在此手工补全识别结论后保存。";
  }
  setDormAIStatusBadge("待宿管核对", "pill-badge-amber");
  box.classList.remove("hidden");
}

function setDormAIStatusBadge(text, cls) {
  const badge = document.getElementById("dorm-ai-status-badge");
  if (badge) badge.className = `pill-badge ${cls} text-[10px]`, badge.innerText = text;
}

async function submitDormInspectionCorrection(e) {
  if (e) e.preventDefault();
  if (!currentDormInspection) {
    toast("当前没有待纠正的识别结果", "warning");
    return;
  }

  const reason = dormFixValue("dorm-fix-reason").trim();
  if (!reason) {
    toast("请先填写纠正理由，它会随记录永久留痕", "warning");
    return;
  }

  const base = currentDormInspection.baseline;
  const payload = { reason };

  const vision = dormFixValue("dorm-fix-vision").trim();
  if (vision !== base.vision_analysis.trim()) payload.vision_analysis = vision;
  const category = dormFixValue("dorm-fix-category").trim();
  if (category !== base.category.trim()) payload.category = category;
  const severity = dormFixValue("dorm-fix-severity");
  if (severity !== base.severity) payload.severity = severity;
  const points = parseInt(dormFixValue("dorm-fix-points"), 10);
  if (Number.isNaN(points) || points < 0 || points > 30) {
    toast("建议扣分必须是 0~30 之间的整数", "warning");
    return;
  }
  if (points !== Number(base.deduct_points)) payload.deduct_points = points;
  const summary = dormFixValue("dorm-fix-summary").trim();
  if (summary !== base.summary.trim()) payload.summary = summary;
  const advice = dormFixValue("dorm-fix-advice").trim();
  if (advice !== base.action_advice.trim()) payload.action_advice = advice;

  const changedKeys = Object.keys(payload).filter(k => k !== "reason");
  if (changedKeys.length === 0) {
    toast("没有检测到任何改动：请先修改识别结论", "warning");
    return;
  }

  const btn = document.getElementById("btn-dorm-fix-submit");
  if (btn) btn.disabled = true;

  const res = await request(`/dorm/inspections/${currentDormInspection.id}/correct`, {
    method: "POST",
    body: JSON.stringify(payload),
  });
  if (btn) btn.disabled = false;

  if (!res || !res.ok) {
    if (res) {
      const err = await res.json().catch(() => ({}));
      toast(err.error || "纠正结果保存失败", "error");
    }
    return;
  }

  const data = await res.json();
  const nb = data.structured_result || {};
  currentDormInspection.baseline = {
    vision_analysis: (data.record && data.record.vision_ai_output) || "",
    category: nb.category || "",
    severity: nb.severity || "medium",
    deduct_points: nb.deduct_points === undefined ? 0 : nb.deduct_points,
    summary: nb.summary || "",
    action_advice: nb.action_advice || "",
  };
  document.getElementById("dorm-fix-reason").value = "";

  document.getElementById("dorm-ai-analysis-text").innerText = currentDormInspection.baseline.vision_analysis;
  document.getElementById("dorm-ai-json-raw").innerText = JSON.stringify(nb, null, 2);

  const hint = document.getElementById("dorm-fix-hint");
  if (hint) hint.innerText = `已纠正 ${(data.changes || []).length} 项并留痕，后续打表以本条为准。`;
  setDormAIStatusBadge("已人工纠正", "pill-badge-green");

  toast(data.message || "识别结论已按人工纠正结果入库", "success");
  loadDormWaterfall();
}

// =============================================================================
// 4. 部员中心逻辑 (重点突出: 请假申报、上工动态分析图、请假/缺工/总积分统计)
// =============================================================================

// 图表全局状态
let memberTrendState = {
  data: [],
  dimension: 'score', // 'score' 或 'hours'
};

async function loadMemberPanel() {
  if (!state.user) return;
  document.getElementById("mem-greeting-name").innerText = `${state.user.real_name}，今日上工状态优良`;
  document.getElementById("mem-dept-tag").innerText = `${state.user.department || '纪检部'} · 当值部员`;

  // 1. 获取积分、请假、缺工与动态趋势指标
  const scoreRes = await request("/member/score-history", { method: "GET" });
  if (scoreRes && scoreRes.ok) {
    const data = await scoreRes.json();
    
    // 四大核心指标
    document.getElementById("mem-total-score").innerText = data.total_score;
    document.getElementById("mem-leave-count").innerText = data.leave_count !== undefined ? data.leave_count : 0;
    document.getElementById("mem-missed-count").innerText = data.missed_count !== undefined ? data.missed_count : 0;
    document.getElementById("mem-duty-count").innerText = data.duty_count;

    // 动态分析图表数据
    if (data.dynamic_trend && data.dynamic_trend.length > 0) {
      memberTrendState.data = data.dynamic_trend;
      renderMemberAttendanceChart();
    }

    // 积分台账明细
    const logList = document.getElementById("mem-score-log-list");
    if (logList) {
      logList.innerHTML = data.score_history.map(l => `
        <div class="p-3 rounded-2xl bg-zinc-50 border border-zinc-100 flex items-center justify-between text-xs">
          <div>
            <div class="font-bold text-black">${l.reason}</div>
            <div class="text-zinc-400 text-[10px] mt-0.5">经办人: ${l.operator_name} · ${l.created_at.slice(0, 10)}</div>
          </div>
          <div class="font-mono font-bold text-sm ${l.score_change >= 0 ? 'text-black' : 'text-zinc-400'}">
            ${l.score_change >= 0 ? '+' : ''}${l.score_change}
          </div>
        </div>
      `).join("");
    }
  }

  // 2. 个人当值排班班次
  const shiftRes = await request("/member/my-shifts", { method: "GET" });
  if (shiftRes && shiftRes.ok) {
    const data = await shiftRes.json();
    const shiftList = document.getElementById("mem-shift-list");
    if (shiftList) {
      shiftList.innerHTML = data.items.map(s => `
        <div class="p-3 rounded-2xl bg-zinc-50 border border-zinc-100 text-xs flex items-center justify-between">
          <div>
            <div class="font-bold text-black">${s.date} ${s.shift_period}</div>
            <div class="text-zinc-500 text-[11px] mt-0.5">${s.building} · 协同宿管: ${s.manager_name}</div>
          </div>
          <span class="pill-badge pill-badge-dark text-[10px]">在岗巡查</span>
        </div>
      `).join("");
    }

    // 填入快速请假选择下拉框
    const select = document.getElementById("inline-leave-shift");
    if (select) {
      select.innerHTML = data.items.map(s => `
        <option value="${s.id}">${s.date} ${s.shift_period} (${s.building})</option>
      `).join("");
      select.onchange = () => refreshAutoSubPreview();
      refreshAutoSubPreview();
    }
  }

  // 3. 请假审批流转历史
  loadMemberLeaveHistory();

  // 4. 组织部查寝打表与扣分存底台账
  loadDeductionsTable();
}

async function loadMemberLeaveHistory() {
  const leaveRes = await request("/member/leave-list", { method: "GET" });
  if (!leaveRes || !leaveRes.ok) return;
  const data = await leaveRes.json();
  
  const countEl = document.getElementById("mem-leave-list-count");
  if (countEl) countEl.innerText = `共 ${data.total} 条记录`;

  const container = document.getElementById("inline-leave-history-list");
  if (!container) return;

  if (data.items.length === 0) {
    container.innerHTML = `
      <div class="p-6 text-center text-zinc-400 text-xs space-y-1 bg-zinc-50 rounded-2xl border border-zinc-100">
        <i class="fa-regular fa-calendar-check text-2xl text-zinc-300"></i>
        <div class="font-bold text-black">暂无请假报备记录</div>
        <div class="text-[11px]">本学期履职考勤全勤中</div>
      </div>
    `;
    return;
  }

  container.innerHTML = data.items.map(l => {
    let statusBadge = `<span class="pill-badge pill-badge-amber text-[10px]">待部长审批</span>`;
    if (l.status === 'approved') statusBadge = `<span class="pill-badge pill-badge-green text-[10px]">已批准同意</span>`;
    if (l.status === 'rejected') statusBadge = `<span class="pill-badge pill-badge-gray text-[10px]">已驳回需补正</span>`;

    return `
      <div class="p-3.5 rounded-2xl bg-zinc-50 border border-zinc-200/80 space-y-1.5 text-xs">
        <div class="flex items-center justify-between">
          <span class="font-bold text-black text-[11px]">${l.shift_info}</span>
          ${statusBadge}
        </div>
        <p class="text-zinc-600 text-[11px]">事由: ${l.reason}</p>
        <div class="text-[10px] text-zinc-400 flex items-center justify-between pt-1 border-t border-zinc-100">
          <span>替班人: ${l.substitute_name || '未指定'}</span>
          <span>${l.created_at ? l.created_at.slice(0, 16) : ''}</span>
        </div>
      </div>
    `;
  }).join("");
}

// 快速填入请假事由
function fillLeaveReason(text) {
  const textarea = document.getElementById("inline-leave-reason");
  if (textarea) textarea.value = text;
}

// 快速请假申报表单提交 (支持智能自动人员替补)
async function handleInlineLeaveSubmit(e) {
  e.preventDefault();
  const shiftID = parseInt(document.getElementById("inline-leave-shift").value);
  const reason = document.getElementById("inline-leave-reason").value.trim();
  const sub = document.getElementById("inline-leave-sub").value.trim();
  const autoSub = document.getElementById("inline-leave-auto-sub") ? document.getElementById("inline-leave-auto-sub").checked : false;

  if (!shiftID) {
    toast("请选择需要请假的排班班次", "warning");
    return;
  }

  const res = await request("/member/leave", {
    method: "POST",
    body: JSON.stringify({
      shift_id: shiftID,
      reason,
      substitute_name: sub,
      auto_substitute: autoSub,
    }),
  });

  if (res && res.ok) {
    const data = await res.json();
    toast(data.message || "请假申请已提交！部长审批通过后将自动更新排班，不计缺工！", "info");
    document.getElementById("form-inline-leave").reset();
    loadMemberPanel();
  }
}

// 切换智能自动人员替补开关
function handleToggleAutoSub(e) {
  const box = document.getElementById("inline-sub-preview-box");
  if (!box) return;
  if (e.target.checked) {
    box.classList.remove("hidden");
    refreshAutoSubPreview();
  } else {
    box.classList.add("hidden");
  }
}

// 运算并预览推荐替补人员
async function refreshAutoSubPreview() {
  const shiftSelect = document.getElementById("inline-leave-shift");
  const previewText = document.getElementById("inline-sub-preview-text");
  if (!previewText) return;

  const shiftID = shiftSelect ? shiftSelect.value : "";
  if (!shiftID) {
    previewText.innerHTML = `<span class="text-zinc-400">请选择上方排班班次以实时预估替补</span>`;
    return;
  }

  previewText.innerHTML = `<i class="fa-solid fa-spinner fa-spin mr-1"></i> 正在根据上工负荷算法匹配最佳替补人员...`;
  const res = await request(`/member/substitute-recommend?shift_id=${shiftID}`, { method: "GET" });
  if (!res || !res.ok) {
    previewText.innerHTML = `<span class="text-zinc-400">未检测到可用候选人员</span>`;
    return;
  }

  const data = await res.json();
  if (data.has_candidate && data.candidate) {
    previewText.innerHTML = `
      <span class="text-emerald-700 font-bold"><i class="fa-solid fa-wand-magic-sparkles mr-1 text-emerald-600"></i> ${data.candidate.reason}</span>
    `;
  } else {
    previewText.innerHTML = `<span class="text-amber-600"><i class="fa-solid fa-circle-info mr-1"></i> ${data.message || '当前同日暂无空闲部员'}</span>`;
  }
}

// 切换动态图表分析维度 (积分 vs 上工时数)
function toggleTrendDimension(dim) {
  memberTrendState.dimension = dim;
  const btnScore = document.getElementById("btn-dim-score");
  const btnHours = document.getElementById("btn-dim-hours");
  if (dim === 'score') {
    btnScore.className = "nav-pill-item active";
    btnHours.className = "nav-pill-item";
  } else {
    btnHours.className = "nav-pill-item active";
    btnScore.className = "nav-pill-item";
  }
  renderMemberAttendanceChart();
}

// =============================================================================
// 原生 Canvas 高质感动态分析图表渲染 (Smooth Bezier Area Chart)
// =============================================================================
function renderMemberAttendanceChart() {
  const canvas = document.getElementById("member-attendance-chart");
  if (!canvas) return;

  const ctx = canvas.getContext("2d");
  const rect = canvas.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;

  canvas.width = rect.width * dpr;
  canvas.height = rect.height * dpr;
  ctx.scale(dpr, dpr);

  const w = rect.width;
  const h = rect.height;
  const padding = { top: 30, right: 30, bottom: 40, left: 45 };

  const data = memberTrendState.data;
  if (!data || data.length === 0) return;

  const isScore = memberTrendState.dimension === 'score';
  const values = data.map(d => isScore ? d.score : d.hours);

  const minVal = isScore ? 95 : 0;
  const maxVal = isScore ? Math.max(...values, 120) + 5 : Math.max(...values, 3) + 0.5;

  ctx.clearRect(0, 0, w, h);

  // 1. 绘制极简水平参考网格线
  ctx.strokeStyle = "rgba(0, 0, 0, 0.05)";
  ctx.lineWidth = 1;
  const gridSteps = 4;
  for (let i = 0; i <= gridSteps; i++) {
    const y = padding.top + (h - padding.top - padding.bottom) * (i / gridSteps);
    ctx.beginPath();
    ctx.moveTo(padding.left, y);
    ctx.lineTo(w - padding.right, y);
    ctx.stroke();

    // 绘制刻度数值
    ctx.fillStyle = "#a1a1aa";
    ctx.font = "10px 'JetBrains Mono', monospace";
    ctx.textAlign = "right";
    const valLabel = (maxVal - (maxVal - minVal) * (i / gridSteps)).toFixed(isScore ? 0 : 1);
    ctx.fillText(isScore ? `${valLabel}分` : `${valLabel}h`, padding.left - 8, y + 3);
  }

  // 2. 计算点坐标
  const chartWidth = w - padding.left - padding.right;
  const chartHeight = h - padding.top - padding.bottom;
  const points = data.map((d, idx) => {
    const val = isScore ? d.score : d.hours;
    const x = padding.left + (chartWidth / (data.length - 1)) * idx;
    const y = padding.top + chartHeight - ((val - minVal) / (maxVal - minVal)) * chartHeight;
    return { x, y, val, date: d.date, label: d.label, hours: d.hours, score: d.score };
  });

  // 3. 绘制平滑渐变填充区域 (Area Gradient)
  const grad = ctx.createLinearGradient(0, padding.top, 0, h - padding.bottom);
  grad.addColorStop(0, "rgba(9, 9, 11, 0.12)");
  grad.addColorStop(1, "rgba(9, 9, 11, 0.0)");

  ctx.beginPath();
  ctx.moveTo(points[0].x, points[0].y);
  for (let i = 0; i < points.length - 1; i++) {
    const xc = (points[i].x + points[i + 1].x) / 2;
    const yc = (points[i].y + points[i + 1].y) / 2;
    ctx.quadraticCurveTo(points[i].x, points[i].y, xc, yc);
  }
  ctx.quadraticCurveTo(points[points.length - 1].x, points[points.length - 1].y, points[points.length - 1].x, points[points.length - 1].y);
  ctx.lineTo(points[points.length - 1].x, h - padding.bottom);
  ctx.lineTo(points[0].x, h - padding.bottom);
  ctx.closePath();
  ctx.fillStyle = grad;
  ctx.fill();

  // 4. 绘制平滑贝塞尔曲线线条
  ctx.beginPath();
  ctx.strokeStyle = "#09090b";
  ctx.lineWidth = 2.5;
  ctx.lineCap = "round";
  ctx.lineJoin = "round";
  ctx.moveTo(points[0].x, points[0].y);
  for (let i = 0; i < points.length - 1; i++) {
    const xc = (points[i].x + points[i + 1].x) / 2;
    const yc = (points[i].y + points[i + 1].y) / 2;
    ctx.quadraticCurveTo(points[i].x, points[i].y, xc, yc);
  }
  ctx.quadraticCurveTo(points[points.length - 1].x, points[points.length - 1].y, points[points.length - 1].x, points[points.length - 1].y);
  ctx.stroke();

  // 5. 绘制关键数据点与底部日期
  points.forEach((pt, idx) => {
    // 底部日期文字
    ctx.fillStyle = "#71717a";
    ctx.font = "10px 'JetBrains Mono', monospace";
    ctx.textAlign = "center";
    ctx.fillText(pt.date, pt.x, h - padding.bottom + 18);

    // 外圈光晕
    ctx.beginPath();
    ctx.arc(pt.x, pt.y, 5, 0, Math.PI * 2);
    ctx.fillStyle = "#ffffff";
    ctx.fill();
    ctx.strokeStyle = "#09090b";
    ctx.lineWidth = 2.5;
    ctx.stroke();
  });

  // 鼠标交互 Tooltip 探测
  canvas.onmousemove = (e) => {
    const r = canvas.getBoundingClientRect();
    const mx = e.clientX - r.left;
    const my = e.clientY - r.top;

    let closest = null;
    let minDist = 30;
    points.forEach(pt => {
      const dist = Math.hypot(pt.x - mx, pt.y - my);
      if (dist < minDist) {
        minDist = dist;
        closest = pt;
      }
    });

    const tip = document.getElementById("chart-hover-tooltip");
    if (closest && tip) {
      tip.classList.remove("hidden");
      tip.style.left = `${closest.x + 12}px`;
      tip.style.top = `${closest.y - 30}px`;
      tip.innerHTML = `
        <div class="font-bold text-white">${closest.label} (${closest.date})</div>
        <div class="text-[10px] text-zinc-300">上工时长: ${closest.hours}h · 履职积分: ${closest.score}分</div>
      `;
    } else if (tip) {
      tip.classList.add("hidden");
    }
  };

  canvas.onmouseleave = () => {
    const tip = document.getElementById("chart-hover-tooltip");
    if (tip) tip.classList.add("hidden");
  };
}

// 窗口 resize 自动重绘
window.addEventListener("resize", () => {
  if (state.currentTab === "member") {
    renderMemberAttendanceChart();
  }
});

// =============================================================================
// 4.2 组织部 · 技术部副部长专属宿管上午上报数据汇总与查寝打表扣分
// =============================================================================
let patrolSearchTimer = null;

// 加载今日上午宿管数据上报监控卡片（包含楼层、宿管名字、提交文本/照片、纪检汇报状态）
async function loadMorningDormReports() {
  const grid = document.getElementById("morning-reports-grid");
  const countBadge = document.getElementById("morning-reports-count-badge");
  if (!grid) return;

  grid.innerHTML = `<div class="col-span-full text-center py-10 text-zinc-400 text-xs"><i class="fa-solid fa-spinner fa-spin mr-1.5"></i> 正在读取宿管今日上午巡检上报数据与照片...</div>`;

  const res = await request("/deductions/morning-dorm-reports", { method: "GET" });
  if (!res || !res.ok) {
    grid.innerHTML = `<div class="col-span-full text-center py-10 text-rose-500 text-xs">加载上午宿管上报失败，请稍后重试</div>`;
    return;
  }

  const data = await res.json();
  const reports = data.items || [];

  if (countBadge) {
    countBadge.innerText = `${data.count || reports.length} 条上报已同步`;
  }

  if (reports.length === 0) {
    grid.innerHTML = `
      <div class="col-span-full text-center py-12 text-zinc-400 space-y-1.5 text-xs">
        <i class="fa-regular fa-folder-open text-2xl text-zinc-300"></i>
        <p>今日上午暂无宿管提交照片或文本数据</p>
        <p class="text-[11px] text-zinc-400">宿管完成拍照上传后将在此处自动呈现现场照片与说明，供技术部副部长核实打表</p>
      </div>
    `;
    return;
  }

  grid.innerHTML = reports.map(r => `
    <div id="report-card-${r.id}" class="p-4 rounded-2xl bg-zinc-50 border border-zinc-200/80 space-y-3 hover:border-black transition flex flex-col justify-between">
      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <span class="pill-badge pill-badge-dark text-[10px] font-bold">
            <i class="fa-solid fa-building mr-1 text-zinc-400"></i> ${r.building} · ${r.floor}
          </span>
          <span class="pill-badge ${r.is_deducted ? 'pill-badge-gray text-zinc-400' : 'pill-badge-green'} text-[9px]">
            ${r.is_deducted ? '已录入打表' : '待核准打表'}
          </span>
        </div>

        <div class="flex items-center justify-between text-xs">
          <span class="font-bold text-black flex items-center gap-1">
            <i class="fa-solid fa-house-chimney-user text-zinc-500 text-[11px]"></i>
            <span>责任宿管: ${r.manager_name}</span>
          </span>
          <span class="text-zinc-400 font-mono text-[10px]">${r.created_at ? r.created_at.slice(11, 16) : ''}</span>
        </div>

        ${r.ai_status === 'real'
          ? `<span class="pill-badge pill-badge-green text-[9px] w-fit"><i class="fa-solid fa-microchip mr-1"></i>AI 已识别，可对照原图核验</span>`
          : r.ai_status === 'disabled'
          ? `<span class="pill-badge pill-badge-amber text-[9px] w-fit"><i class="fa-solid fa-plug-circle-xmark mr-1"></i>未启用 AI，需人工看图录入</span>`
          : r.ai_status === 'failed'
          ? `<span class="pill-badge pill-badge-amber text-[9px] w-fit"><i class="fa-solid fa-triangle-exclamation mr-1"></i>AI 调用失败，需人工看图录入</span>`
          : `<span class="pill-badge pill-badge-gray text-[9px] w-fit"><i class="fa-solid fa-circle-question mr-1"></i>识别状态未知，按人工核对处理</span>`}

        ${(r.photo_url || r.image_url) ? `
          <div class="w-full h-36 rounded-xl overflow-hidden bg-black/5 border border-zinc-200 relative group cursor-pointer" onclick="window.open('${r.photo_url || r.image_url}', '_blank')">
            <img src="${r.photo_url || r.image_url}" alt="现场照片" class="w-full h-full object-cover group-hover:scale-105 transition duration-300">
            <div class="absolute inset-0 bg-black/30 opacity-0 group-hover:opacity-100 transition flex items-center justify-center text-white text-xs font-bold gap-1">
              <i class="fa-solid fa-magnifying-glass-plus"></i> 查看大图
            </div>
          </div>
        ` : `
          <div class="w-full h-20 rounded-xl bg-zinc-100 border border-dashed border-zinc-200 flex items-center justify-center text-zinc-400 text-xs">
            <i class="fa-regular fa-image mr-1 text-zinc-300"></i> 纯文本上报（未附照片）
          </div>
        `}

        <div class="p-2.5 rounded-xl bg-white border border-zinc-100 text-xs text-zinc-700 space-y-1">
          <div class="text-[10px] font-bold text-zinc-400 flex items-center justify-between">
            <span>${r.report_kind === 'note' ? '记名纸条转录正文' : '宿管填报文本事实说明'}</span>
            <span class="font-mono text-black">${r.room_number ? r.room_number + '室' : ''}</span>
          </div>
          <p class="leading-relaxed line-clamp-3" title="${escapeAttr(r.submitted_text)}">
            ${escapeHtml(r.submitted_text || '无补充文本描述')}
          </p>
        </div>

        ${(r.subject_total > 0) ? `
          <div class="p-2.5 rounded-xl bg-white border border-zinc-100 text-xs space-y-1.5">
            <div class="text-[10px] font-bold text-zinc-400 flex items-center justify-between">
              <span>被记名学生名单（勾选后批量打表）</span>
              <span class="font-mono text-black">共 ${r.subject_total} 人</span>
            </div>
            ${(r.subjects || []).map(s => `
              <label class="flex items-start gap-2 ${s.converted_deduction_id ? 'opacity-45' : ''}">
                <input type="checkbox" class="mt-1 accent-black report-subject-chk" value="${s.id}"
                  ${s.match_status === 'matched' && !s.converted_deduction_id ? 'checked' : ''}
                  ${s.converted_deduction_id ? 'disabled' : ''}>
                <span class="leading-tight">
                  <b class="text-black">${s.raw_name}</b>
                  ${s.class_name ? `<span class="text-zinc-400">· ${s.class_name}</span>` : ''}
                  ${s.match_status === 'matched'
                    ? '<span class="text-emerald-600 font-bold">· 名册已核对</span>'
                    : s.match_status === 'ambiguous'
                    ? '<span class="text-amber-600 font-bold">· 同名需指定</span>'
                    : '<span class="text-red-600 font-bold">· 名册查无此人</span>'}
                  ${s.converted_deduction_id ? `<span class="text-zinc-400">· 已扣分 #${s.converted_deduction_id}</span>` : ''}
                  ${s.match_note ? `<span class="block text-[10px] text-zinc-400">${s.match_note}</span>` : ''}
                </span>
              </label>
            `).join('')}
          </div>
        ` : ''}
      </div>

      <div class="pt-2 border-t border-zinc-200/60 flex items-center justify-between gap-2">
        <span class="text-[10px] text-zinc-400">打表授权: 技术副部长</span>
        ${r.subject_total > 0 ? `
          <button onclick="convertReportSubjects(${r.id})" class="btn-pill btn-pill-dark text-[11px] py-1 px-3 font-bold bg-black text-white hover:scale-105 transition">
            <i class="fa-solid fa-list-check mr-1 text-amber-400"></i> 按勾选名单批量打表
          </button>
        ` : `
          <button onclick="fillDeductionFromMorningReport('${escapeJs(r.building)}', '${escapeJs(r.floor)}', '${escapeJs(r.room_number || '')}', '${escapeJs(r.submitted_text || '')}')" class="btn-pill btn-pill-dark text-[11px] py-1 px-3 font-bold bg-black text-white hover:scale-105 transition">
            <i class="fa-solid fa-pen-to-square mr-1 text-amber-400"></i> 转入打表扣分
          </button>
        `}
      </div>
    </div>
  `).join("");
}

// 把上报卡片中勾选的名单条目批量转入打表：类别/分值/事由取自下方打表单
async function convertReportSubjects(inspectionId) {
  const card = document.getElementById(`report-card-${inspectionId}`);
  const checked = card ? Array.from(card.querySelectorAll(".report-subject-chk:checked")) : [];
  if (checked.length === 0) {
    toast("请先在名单中勾选至少一名学生", "warning");
    return;
  }

  const category = document.getElementById("add-deduct-cat")?.value;
  const points = parseInt(document.getElementById("add-deduct-points")?.value, 10) || 0;
  const reason = (document.getElementById("add-deduct-reason")?.value || "").trim();
  const floor = (document.getElementById("add-deduct-floor")?.value || "").trim();

  if (!category || !reason || points <= 0) {
    toggleDeductionForm(true);
    toast("请先在下方打表单填写违纪类别、分值与事由，再批量转入名单", "warning");
    return;
  }

  if (!confirm(`将为勾选的 ${checked.length} 名学生各扣 ${points} 分，并生成打表记录。确认继续？`)) return;

  const confirmPwd = prompt("批量写入扣分为高危操作，请输入当前登录口令二次确认：");
  if (confirmPwd === null) return;
  if (!confirmPwd.trim()) {
    toast("口令不能为空，操作已取消", "warning");
    return;
  }

  const res = await request("/deductions/from-report", {
    method: "POST",
    headers: { "X-Confirm-Password": confirmPwd },
    body: JSON.stringify({
      inspection_id: inspectionId,
      subject_ids: checked.map(el => parseInt(el.value, 10)),
      floor: floor,
      category: category,
      deduct_points: points,
      reason: reason,
    }),
  });

  const data = res ? await res.json() : null;
  if (res && res.ok) {
    toast(data.message || "批量打表完成", "success");
    document.getElementById("add-deduct-reason").value = "";
    loadMorningDormReports();
    loadDeductionsTable();
  } else if (data) {
    toast(data.error || "批量打表失败", "error");
  }
}

function escapeJs(value) {
  return String(value ?? "").replace(/\\/g, "\\\\").replace(/'/g, "\\'").replace(/\r?\n/g, " ");
}

function escapeAttr(value) {
  return String(value ?? "").replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
}

// 快速将上午宿管上报转入打表表单
function fillDeductionFromMorningReport(bldg, floor, room, text) {
  toggleDeductionForm(true);
  const bldgInp = document.getElementById("add-deduct-bldg");
  const floorInp = document.getElementById("add-deduct-floor");
  const roomInp = document.getElementById("add-deduct-room");
  const reasonInp = document.getElementById("add-deduct-reason");

  if (bldgInp) bldgInp.value = bldg;
  if (floorInp) floorInp.value = floor;
  if (roomInp && room) roomInp.value = room;
  if (reasonInp) reasonInp.value = `[宿管上午上报核准] ${text}`;

  const nameInp = document.getElementById("add-deduct-name");
  if (nameInp) nameInp.focus();
}

function toggleDeductionForm(forceOpen = false) {
  const box = document.getElementById("box-deduction-add-form");
  const btn = document.getElementById("btn-toggle-deduction-form");
  if (!box) return;
  if (forceOpen || box.classList.contains("hidden")) {
    box.classList.remove("hidden");
    if (btn) btn.innerHTML = `<i class="fa-solid fa-chevron-up mr-1"></i> 收起打表单`;
    document.getElementById("add-deduct-room")?.focus();
  } else {
    box.classList.add("hidden");
    if (btn) btn.innerHTML = `<i class="fa-solid fa-plus mr-1"></i> 手动录入打表扣分`;
  }
}

function setDeductPoints(pts) {
  const inp = document.getElementById("add-deduct-points");
  if (inp) inp.value = pts;
}

async function handleCreateDeductionSubmit(e) {
  e.preventDefault();
  const confirmPwd = prompt("写入扣分为高危操作，请输入当前登录口令二次确认：");
  if (confirmPwd === null) return;
  if (!confirmPwd.trim()) {
    toast("口令不能为空，操作已取消", "warning");
    return;
  }

  const payload = {
    building: document.getElementById("add-deduct-bldg").value.trim(),
    floor: document.getElementById("add-deduct-floor").value.trim(),
    room_number: document.getElementById("add-deduct-room").value.trim(),
    student_name: document.getElementById("add-deduct-name").value.trim(),
    class_name: document.getElementById("add-deduct-class").value.trim(),
    category: document.getElementById("add-deduct-cat").value,
    deduct_points: parseInt(document.getElementById("add-deduct-points").value, 10) || 2,
    reason: document.getElementById("add-deduct-reason").value.trim(),
  };

  const res = await request("/deductions", {
    method: "POST",
    headers: { "X-Confirm-Password": confirmPwd },
    body: JSON.stringify(payload),
  });

  if (res && res.ok) {
    const data = await res.json();
    toast(data.message || "打表记录已成功录入入库！", "success");
    // 清理违纪学生姓名、寝室与原因，保留楼栋楼层与班级便于连打
    document.getElementById("add-deduct-name").value = "";
    document.getElementById("add-deduct-reason").value = "";
    document.getElementById("add-deduct-room").focus();
    loadDeductionsTable();
  } else if (res) {
    const err = await res.json();
    toast(err.error || "录入打表失败，请检查网络", "error");
  }
}

function debounceDeductionFilter() {
  clearTimeout(patrolSearchTimer);
  patrolSearchTimer = setTimeout(() => {
    loadDeductionsTable();
  }, 200);
}

function clearDeductionFilters() {
  const f = document.getElementById("filter-deduct-floor");
  const r = document.getElementById("filter-deduct-room");
  const n = document.getElementById("filter-deduct-name");
  const c = document.getElementById("filter-deduct-class");
  const cat = document.getElementById("filter-deduct-cat");
  if (f) f.value = "";
  if (r) r.value = "";
  if (n) n.value = "";
  if (c) c.value = "";
  if (cat) cat.value = "";
  loadDeductionsTable();
}

async function loadDeductionsTable() {
  const floor = document.getElementById("filter-deduct-floor")?.value.trim() || "";
  const room = document.getElementById("filter-deduct-room")?.value.trim() || "";
  const name = document.getElementById("filter-deduct-name")?.value.trim() || "";
  const cls = document.getElementById("filter-deduct-class")?.value.trim() || "";
  const cat = document.getElementById("filter-deduct-cat")?.value || "";

  const params = new URLSearchParams();
  if (floor) params.append("floor", floor);
  if (room) params.append("room", room);
  if (name) params.append("name", name);
  if (cls) params.append("class", cls);
  if (cat) params.append("category", cat);

  const res = await request(`/deductions?${params.toString()}`, { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();

  const statsEl = document.getElementById("deduct-table-stats-summary");
  if (statsEl) {
    statsEl.innerText = `已检索加载 ${data.total} 条打表存底 · 累计扣分 ${data.total_deduct_sum} 分`;
  }

  const wrap = document.getElementById("deduct-records-table-wrap");
  if (!wrap) return;

  if (!data.items || data.items.length === 0) {
    wrap.innerHTML = `
      <div class="py-10 text-center text-zinc-400 space-y-1 text-xs">
        <i class="fa-solid fa-clipboard-check text-xl text-zinc-300"></i>
        <p>当前筛选条件暂无打表扣分记录</p>
      </div>
    `;
    return;
  }

  wrap.innerHTML = `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
        <tr>
          <th class="p-2.5 font-mono text-zinc-400">流水号</th>
          <th class="p-2.5">楼栋/楼层</th>
          <th class="p-2.5">寝室号</th>
          <th class="p-2.5">违规学生</th>
          <th class="p-2.5">班级</th>
          <th class="p-2.5">违纪类别</th>
          <th class="p-2.5">扣分</th>
          <th class="p-2.5">事实情形</th>
          <th class="p-2.5">打表人</th>
          <th class="p-2.5">记录时间</th>
          <th class="p-2.5 text-right">操作</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100">
        ${data.items.map(d => `
          <tr class="hover:bg-zinc-50/80 transition">
            <td class="p-2.5 font-mono text-zinc-400 text-[11px]">#${d.id}</td>
            <td class="p-2.5 font-semibold text-black">${escapeHtml(d.building)} · ${escapeHtml(d.floor)}</td>
            <td class="p-2.5 font-mono font-bold text-black">${escapeHtml(d.room_number)}室</td>
            <td class="p-2.5 font-bold text-black">${escapeHtml(d.student_name)}</td>
            <td class="p-2.5 font-medium text-zinc-700">${escapeHtml(d.class_name)}</td>
            <td class="p-2.5"><span class="pill-badge pill-badge-dark text-[10px]">${escapeHtml(d.category)}</span></td>
            <td class="p-2.5 font-mono font-black text-rose-600 text-sm">-${d.deduct_points}</td>
            <td class="p-2.5 text-zinc-600 max-w-xs truncate" title="${escapeAttr(d.reason)}">${escapeHtml(d.reason)}</td>
            <td class="p-2.5 text-zinc-500">${escapeHtml(d.inspector_name)}</td>
            <td class="p-2.5 font-mono text-zinc-400 text-[10px] whitespace-nowrap">${d.created_at.slice(0, 16).replace('T', ' ')}</td>
            <td class="p-2.5 text-right">
              <button onclick="deleteDeductionRecord(${d.id})" class="btn-pill btn-pill-light text-xs py-0.5 px-2 text-rose-500 hover:text-rose-600 hover:bg-rose-50" title="撤销此条记录">
                <i class="fa-regular fa-trash-can"></i>
              </button>
            </td>
          </tr>
        `).join("")}
      </tbody>
    </table>
  `;
}

function escapeHtml(value) {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

async function downloadDeductionsCSV() {
  if (!state.user) {
    toast("请先登录系统", "warning");
    return;
  }
  const floor = document.getElementById("filter-deduct-floor")?.value.trim() || "";
  const room = document.getElementById("filter-deduct-room")?.value.trim() || "";
  const name = document.getElementById("filter-deduct-name")?.value.trim() || "";
  const cls = document.getElementById("filter-deduct-class")?.value.trim() || "";
  const cat = document.getElementById("filter-deduct-cat")?.value || "";

  const params = new URLSearchParams();
  if (floor) params.append("floor", floor);
  if (room) params.append("room", room);
  if (name) params.append("name", name);
  if (cls) params.append("class", cls);
  if (cat) params.append("category", cat);

  await downloadAuthedFile(
    `${API_BASE}/deductions/export-csv?${params.toString()}`,
    `学管会组织部打表扣分单_${new Date().toISOString().slice(0, 10)}.csv`,
    "导出打表单失败"
  );
}

async function deleteDeductionRecord(id) {
  const reason = prompt(`撤销打表记录 #${id} 必须填写理由（将永久留痕，原记录不会被删除）：\n例如：误录、学生姓名写错、已复核不构成违纪`);
  if (reason === null) return;
  if (!reason.trim()) {
    toast("撤销理由不能为空", "warning");
    return;
  }
  const confirmPwd = prompt("撤销扣分为高危操作，请输入当前登录口令二次确认：");
  if (confirmPwd === null) return;
  if (!confirmPwd.trim()) {
    toast("口令不能为空，操作已取消", "warning");
    return;
  }

  const res = await request(`/deductions/${id}/revoke`, {
    method: "POST",
    headers: { "X-Confirm-Password": confirmPwd },
    body: JSON.stringify({ reason: reason.trim() }),
  });

  const data = res ? await res.json().catch(() => ({})) : null;
  if (res && res.ok) {
    toast(data.message || "记录已撤销并保留留痕", "success");
    loadDeductionsTable();
    loadMorningDormReports();
  } else if (data) {
    toast(data.error || "撤销失败", "error");
  }
}

// 上报形式切换时调整必填提示：纯文本形式不强制照片
function handleReportKindChange() {
  const kind = document.getElementById("dorm-sel-kind")?.value;
  const picker = document.getElementById("camera-prompt");
  if (picker && kind === "text") {
    picker.querySelector(".text-base")?.classList.add("opacity-50");
  }
}

// =============================================================================
// 5. 部长工作台中枢 (角色: minister)
// =============================================================================
let ministerCurrentSubTab = "overview";
let aiScheduleChatHistory = [];
let aiScheduleLastError = "";
let aiScheduleUploadedImage = "";
let currentAISuggestedShifts = [];

function switchMinisterSubTab(tab) {
  ministerCurrentSubTab = tab;
  const buttons = {
    overview: document.getElementById("btn-minister-sub-overview"),
    "members-mgr": document.getElementById("btn-minister-sub-members-mgr"),
    recruit: document.getElementById("btn-minister-sub-recruit"),
    "ai-schedule": document.getElementById("btn-minister-sub-ai-schedule"),
  };
  const views = {
    overview: document.getElementById("minister-subview-overview"),
    "members-mgr": document.getElementById("minister-subview-members-mgr"),
    recruit: document.getElementById("minister-subview-recruit"),
    "ai-schedule": document.getElementById("minister-subview-ai-schedule"),
  };

  Object.values(buttons).forEach(btn => { if (btn) btn.className = "nav-pill-item"; });
  Object.values(views).forEach(view => { if (view) view.classList.add("hidden"); });

  if (buttons[tab]) buttons[tab].className = "nav-pill-item active";
  if (views[tab]) views[tab].classList.remove("hidden");

  if (tab === "overview") loadMinisterPanel();
  else if (tab === "members-mgr") loadMinisterMembersManagementTable();
  else if (tab === "recruit") loadRecruitApplications(1);
}

// -----------------------------------------------------------------------------
// 5.1 招新报名审核 (文档八章)
// -----------------------------------------------------------------------------
const RECRUIT_STATUS_LABEL = {
  submitted: "待初审",
  shortlisted: "已入围",
  interviewed: "已面试",
  admitted: "已录取",
  rejected: "已淘汰",
};

const RECRUIT_STATUS_BADGE = {
  submitted: "pill-badge-gray",
  shortlisted: "pill-badge-amber",
  interviewed: "pill-badge-dark",
  admitted: "pill-badge-green",
  rejected: "pill-badge-gray",
};

// 每份报名可推进到的下一步；淘汰在所有未完成状态都可执行
const RECRUIT_NEXT_STEPS = {
  submitted: ["shortlisted"],
  shortlisted: ["interviewed"],
  interviewed: ["admitted"],
  admitted: [],
  rejected: ["submitted"],
};

let recruitAppState = { page: 1, pageSize: 20, total: 0 };

async function loadRecruitApplications(page) {
  const wrap = document.getElementById("recruit-app-table-wrap");
  if (!wrap) return;
  wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs"><i class="fa-solid fa-spinner fa-spin mr-1"></i> 正在读取招新报名库...</div>`;

  const q = ((document.getElementById("recruit-filter-q") || {}).value || "").trim();
  const status = ((document.getElementById("recruit-filter-status") || {}).value || "").trim();
  const params = new URLSearchParams({ page: page || 1, page_size: recruitAppState.pageSize });
  if (q) params.set("q", q);
  if (status) params.set("status", status);

  const res = await request(`/minister/recruit/applications?${params.toString()}`, { method: "GET" });
  if (!res) {
    wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs">未加载报名列表：账号权限不足或会话已失效</div>`;
    return;
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    wrap.innerHTML = `<div class="text-center py-12 text-rose-500 text-xs">${escapeHtml(err.error || "报名列表读取失败")}</div>`;
    return;
  }

  const data = await res.json();
  recruitAppState = { page: data.page, pageSize: data.page_size, total: data.total };
  renderRecruitStats(data.status_stats || []);

  const items = data.items || [];
  if (items.length === 0) {
    wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs">当前筛选条件下没有报名记录</div>`;
    renderRecruitPager();
    return;
  }

  wrap.innerHTML = `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
        <tr>
          <th class="p-2.5">报名人</th>
          <th class="p-2.5">班级 / 床位</th>
          <th class="p-2.5">意向部门</th>
          <th class="p-2.5">联系方式</th>
          <th class="p-2.5">申报时间</th>
          <th class="p-2.5">当前状态</th>
          <th class="p-2.5">审核意见</th>
          <th class="p-2.5 text-right">审核操作</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100 align-top">
        ${items.map(a => `
          <tr>
            <td class="p-2.5">
              <div class="font-bold text-black">${escapeHtml(a.real_name)}</div>
              <div class="text-[10px] text-zinc-400">${escapeHtml(a.gender || "性别未填")}</div>
            </td>
            <td class="p-2.5">
              <div>${escapeHtml(a.major_and_class)}</div>
              <div class="text-[10px] text-zinc-400 font-mono">${escapeHtml(a.building_room)}</div>
            </td>
            <td class="p-2.5 font-bold">${escapeHtml(a.target_department)}</td>
            <td class="p-2.5 font-mono text-zinc-600">${escapeHtml(a.phone)}</td>
            <td class="p-2.5 text-zinc-500 font-mono text-[10px]">${formatRecruitTime(a.created_at)}</td>
            <td class="p-2.5">${renderRecruitStatusBadge(a.status)}</td>
            <td class="p-2.5 max-w-[200px] text-zinc-500">
              ${a.interview_feedback ? `<span title="${escapeAttr(a.interview_feedback)}">${escapeHtml(a.interview_feedback)}</span>` : '<span class="text-zinc-300">—</span>'}
              <div class="mt-1 text-[10px] text-zinc-400 leading-snug">${escapeHtml((a.self_introduction || "").slice(0, 40))}</div>
            </td>
            <td class="p-2.5 text-right space-x-1 whitespace-nowrap">
              ${(RECRUIT_NEXT_STEPS[a.status] || []).map(next => `
                <button onclick="reviewRecruit(${a.id}, '${next}')" class="btn-pill btn-pill-dark text-[10px] py-1 px-2.5">${RECRUIT_STATUS_LABEL[next]}</button>
              `).join("")}
              ${a.status !== "rejected" && a.status !== "admitted"
                ? `<button onclick="reviewRecruit(${a.id}, 'rejected')" class="btn-pill btn-pill-light text-[10px] py-1 px-2.5 text-red-500 hover:bg-red-50">淘汰</button>`
                : ""}
            </td>
          </tr>
        `).join("")}
      </tbody>
    </table>
  `;
  renderRecruitPager();
}

function renderRecruitStatusBadge(status) {
  const label = RECRUIT_STATUS_LABEL[status] || status || "未知";
  const cls = RECRUIT_STATUS_BADGE[status] || "pill-badge-gray";
  const danger = status === "rejected" ? "text-rose-600" : "";
  return `<span class="pill-badge ${cls} text-[10px] ${danger}">${escapeHtml(label)}</span>`;
}

function formatRecruitTime(value) {
  if (!value) return "—";
  const d = new Date(value);
  if (isNaN(d.getTime())) return escapeHtml(value);
  return d.toLocaleString("zh-CN", { hour12: false });
}

function renderRecruitStats(stats) {
  const bar = document.getElementById("recruit-status-stats-bar");
  if (!bar) return;
  const map = {};
  stats.forEach(s => { map[s.status] = s.count; });
  const order = ["submitted", "shortlisted", "interviewed", "admitted", "rejected"];
  bar.innerHTML = order.map(key => `
    <button onclick="setRecruitStatusFilter('${key}')" class="pill-badge ${RECRUIT_STATUS_BADGE[key]} text-[10px] hover:opacity-80" title="点击只看该状态的报名">
      ${RECRUIT_STATUS_LABEL[key]} <span class="font-mono ml-1">${map[key] || 0}</span>
    </button>
  `).join("");
}

function setRecruitStatusFilter(status) {
  const sel = document.getElementById("recruit-filter-status");
  if (sel) sel.value = sel.value === status ? "" : status;
  loadRecruitApplications(1);
}

function renderRecruitPager() {
  const pager = document.getElementById("recruit-pager");
  if (!pager) return;
  const pages = Math.max(1, Math.ceil(recruitAppState.total / recruitAppState.pageSize));
  pager.innerHTML = `
    <span>共 ${recruitAppState.total} 份报名 · 第 ${recruitAppState.page} / ${pages} 页</span>
    <span class="space-x-2">
      <button onclick="loadRecruitApplications(${recruitAppState.page - 1})" class="btn-pill btn-pill-light text-[10px] py-1 px-3 ${recruitAppState.page <= 1 ? "opacity-40 pointer-events-none" : ""}">上一页</button>
      <button onclick="loadRecruitApplications(${recruitAppState.page + 1})" class="btn-pill btn-pill-light text-[10px] py-1 px-3 ${recruitAppState.page >= pages ? "opacity-40 pointer-events-none" : ""}">下一页</button>
    </span>
  `;
}

async function reviewRecruit(appID, status) {
  const label = RECRUIT_STATUS_LABEL[status] || status;
  const feedback = prompt(`将报名 #${appID} 的审核状态更新为【${label}】。\n可填写审核意见（仅记录在系统内，报名人不会收到通知），留空则不填写：`, "");
  if (feedback === null) return;

  const res = await request(`/minister/recruit/applications/${appID}/review`, {
    method: "POST",
    body: JSON.stringify({ status, feedback: feedback.trim() }),
  });
  if (!res) return;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    toast(data.error || "审核状态更新失败", "error");
    return;
  }
  toast(data.message || "审核状态已更新", "success");
  await loadRecruitApplications(recruitAppState.page);
}

// 加载部长管辖部门的部员名册与副部长任免操作
async function loadMinisterMembersManagementTable() {
  const wrap = document.getElementById("minister-promote-table-wrap");
  if (!wrap) return;
  wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs"><i class="fa-solid fa-spinner fa-spin mr-1"></i> 正在读取本部部员花名册...</div>`;

  const res = await request("/minister/members", { method: "GET" });
  if (!res || !res.ok) {
    wrap.innerHTML = `<div class="text-center py-12 text-rose-500 text-xs">加载部员列表失败，请稍后重试</div>`;
    return;
  }

  const data = await res.json();
  const members = data.items || [];

  if (members.length === 0) {
    wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs">当前部门暂无在册部员</div>`;
    return;
  }

  wrap.innerHTML = `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
        <tr>
          <th class="p-2.5">部员姓名</th>
          <th class="p-2.5">所属部门</th>
          <th class="p-2.5">当前职务</th>
          <th class="p-2.5">联系电话</th>
          <th class="p-2.5">履职总积分</th>
          <th class="p-2.5">累计上工</th>
          <th class="p-2.5">专属打表权限</th>
          <th class="p-2.5 text-right">任命与职务变更</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100">
        ${members.map(m => {
          const isVice = (m.position === "副部长");
          const isTech = (m.department || "").includes("技术");
          const hasDeductAuth = (isVice && isTech) || m.role === "tech_admin";

          return `
            <tr class="hover:bg-zinc-50 transition">
              <td class="p-2.5 font-bold text-black flex items-center gap-1.5">
                <i class="fa-solid fa-user text-zinc-400"></i>
                <span>${m.real_name}</span>
              </td>
              <td class="p-2.5 text-zinc-700 font-medium">${m.department}</td>
              <td class="p-2.5">
                <span class="pill-badge ${isVice ? 'pill-badge-dark bg-indigo-900 text-white' : 'pill-badge-gray'} text-[10px]">
                  ${m.position || '部员'}
                </span>
              </td>
              <td class="p-2.5 font-mono text-zinc-400">${m.phone}</td>
              <td class="p-2.5 font-mono font-bold text-black">${m.total_score} 分</td>
              <td class="p-2.5 font-mono text-zinc-600">${m.duty_count} 班次</td>
              <td class="p-2.5">
                <span class="pill-badge ${hasDeductAuth ? 'pill-badge-green' : 'pill-badge-gray'} text-[9px]">
                  ${hasDeductAuth ? '已开通宿管上午数据打表' : '无打表权 (不打表)'}
                </span>
              </td>
              <td class="p-2.5 text-right space-x-1.5">
                ${!isVice ? `
                  <button onclick="promoteDepartmentMember(${m.id}, '副部长', '${m.real_name}', '${m.department}')" class="btn-pill btn-pill-dark text-[11px] py-1 px-3 font-bold bg-indigo-600 hover:bg-indigo-700 text-white shadow-sm">
                    <i class="fa-solid fa-crown mr-1 text-amber-300"></i> 升职为副部长
                  </button>
                ` : `
                  <button onclick="promoteDepartmentMember(${m.id}, '部员', '${m.real_name}', '${m.department}')" class="btn-pill btn-pill-light text-[11px] py-1 px-3 text-zinc-600 hover:text-rose-600">
                    降为常规部员
                  </button>
                `}
              </td>
            </tr>
          `;
        }).join("")}
      </tbody>
    </table>
  `;
}

// 提交部员任命
async function promoteDepartmentMember(memberId, targetPosition, memberName, deptName) {
  const isPromotingToVice = (targetPosition === "副部长");
  const actionText = isPromotingToVice ? "升职任命为【副部长】" : "降为常规【部员】";
  let promptMsg = `确定要将部员【${memberName}】(${deptName}) ${actionText} 吗？`;
  if (isPromotingToVice && deptName.includes("技术")) {
    promptMsg += "\n✨ 升职后，该技术部副部长将专属享有【宿管上午数据打表汇总与扣分核验】权限！";
  }

  if (!confirm(promptMsg)) return;

  const confirmPwd = prompt("变更职务为高危操作，请输入当前登录口令二次确认：");
  if (confirmPwd === null) return;
  if (!confirmPwd.trim()) {
    toast("口令不能为空，操作已取消", "warning");
    return;
  }

  const res = await request("/minister/members/promote", {
    method: "POST",
    headers: { "X-Confirm-Password": confirmPwd },
    body: JSON.stringify({
      member_id: memberId,
      position: targetPosition,
    }),
  });

  if (res && res.ok) {
    const data = await res.json();
    toast("🎉 " + data.message, "success");
    loadMinisterMembersManagementTable();
    loadMinisterPanel();
  } else if (res) {
    const err = await res.json();
    toast("操作失败: " + (err.error || "未知原因"), "error");
  }
}

function quickFillSchedulePrompt(text) {
  const inp = document.getElementById("ai-schedule-input-text");
  if (inp) {
    inp.value = text;
    inp.focus();
  }
}

function handleScheduleImageUpload(event) {
  const file = event.target.files[0];
  if (!file) return;

  const reader = new FileReader();
  reader.onload = (e) => {
    aiScheduleUploadedImage = e.target.result;
    const box = document.getElementById("ai-schedule-img-preview-box");
    const thumb = document.getElementById("ai-schedule-img-thumb");
    if (box && thumb) {
      thumb.src = e.target.result;
      box.classList.remove("hidden");
    }
  };
  reader.readAsDataURL(file);
}

function clearScheduleImagePreview() {
  aiScheduleUploadedImage = "";
  const box = document.getElementById("ai-schedule-img-preview-box");
  const inp = document.getElementById("ai-schedule-file-input");
  if (box) box.classList.add("hidden");
  if (inp) inp.value = "";
}

async function handleAIScheduleChatSubmit(e) {
  e.preventDefault();
  const inp = document.getElementById("ai-schedule-input-text");
  const content = inp.value.trim();
  if (!content) return;

  // 追加到多轮上下文
  aiScheduleChatHistory.push({ role: "user", content: content });
  renderAIScheduleChatMessages();

  inp.value = "";
  const btn = document.getElementById("btn-ai-schedule-send");
  if (btn) {
    btn.disabled = true;
    btn.innerHTML = `<i class="fa-solid fa-spinner animate-spin"></i> <span>AI 正在匹配真实部员并编排班次...</span>`;
  }

  const payload = {
    messages: aiScheduleChatHistory,
    image_url: aiScheduleUploadedImage,
  };

  const res = await request("/minister/ai-schedule/chat", {
    method: "POST",
    body: JSON.stringify(payload),
  });

  if (btn) {
    btn.disabled = false;
    btn.innerHTML = `<i class="fa-solid fa-paper-plane"></i> <span>发送排表指令并让 AI 调整班次</span>`;
  }

  if (res && res.ok) {
    const data = await res.json();
    aiScheduleLastError = "";
    aiScheduleChatHistory.push({ role: "assistant", content: data.reply });
    renderAIScheduleChatMessages();

    // 清空图片预览
    clearScheduleImagePreview();

    // 渲染右侧班次表格
    currentAISuggestedShifts = data.suggested_shifts || [];
    renderAIScheduleTable(currentAISuggestedShifts, data.real_members || []);
  } else if (res) {
    const err = await res.json().catch(() => ({}));
    // 本轮没有真实回复，把用户那句话从上下文退回，重试时不会带上半截对话
    aiScheduleChatHistory.pop();
    aiScheduleLastError = err.error || "排表 AI 请求异常";
    renderAIScheduleChatMessages();
    toast("排表 AI 请求异常: " + aiScheduleLastError, "error", 6000);
  }
}

function renderAIScheduleChatMessages() {
  const container = document.getElementById("ai-schedule-chat-messages");
  if (!container) return;

  container.innerHTML = aiScheduleChatHistory.map(m => {
    if (m.role === "user") {
      return `
        <div class="p-3 rounded-2xl bg-black text-white ml-6 space-y-1 text-xs">
          <div class="font-bold text-[10px] text-zinc-400">部长排表指令：</div>
          <p class="leading-relaxed whitespace-pre-wrap">${escapeHtml(m.content)}</p>
        </div>
      `;
    } else {
      return `
        <div class="p-3.5 rounded-2xl bg-zinc-100 text-zinc-800 mr-6 space-y-1 text-xs border border-zinc-200">
          <div class="font-bold text-black flex items-center gap-1.5 text-[11px]">
            <i class="fa-solid fa-robot text-sky-600"></i> AI 排表回复：
          </div>
          <div class="leading-relaxed whitespace-pre-wrap">${escapeHtml(m.content)}</div>
        </div>
      `;
    }
  }).join("") + (aiScheduleLastError ? `
        <div class="p-3.5 rounded-2xl bg-red-50 text-red-800 mr-6 space-y-1 text-xs border border-red-200">
          <div class="font-bold flex items-center gap-1.5 text-[11px] text-red-700">
            <i class="fa-solid fa-plug-circle-xmark"></i> 本轮未生成草案
          </div>
          <div class="leading-relaxed whitespace-pre-wrap">${escapeHtml(aiScheduleLastError)}</div>
        </div>
      ` : "");

  container.scrollTop = container.scrollHeight;
}

function renderAIScheduleTable(shifts, realMembers) {
  const wrap = document.getElementById("ai-schedule-table-wrap");
  const badge = document.getElementById("ai-schedule-shifts-count-badge");
  const btnApply = document.getElementById("btn-apply-ai-schedule");

  if (badge) badge.innerText = `${shifts.length} 个建议班次`;

  if (!shifts || shifts.length === 0) {
    if (wrap) wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs">暂未生成建议班次</div>`;
    if (btnApply) {
      btnApply.disabled = true;
      btnApply.className = "btn-pill btn-pill-dark text-xs py-2 px-4 font-bold shadow-md opacity-50 cursor-not-allowed flex items-center gap-1.5";
    }
    return;
  }

  if (btnApply) {
    btnApply.disabled = false;
    btnApply.className = "btn-pill btn-pill-dark text-xs py-2 px-4 font-bold shadow-md flex items-center gap-1.5 hover:scale-105 active:scale-95 transition bg-black text-white";
  }

  if (wrap) {
    wrap.innerHTML = `
      <table class="w-full text-xs text-left">
        <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
          <tr>
            <th class="p-2.5">日期</th>
            <th class="p-2.5">值班时段</th>
            <th class="p-2.5">负责楼栋</th>
            <th class="p-2.5">指定真实部员</th>
            <th class="p-2.5">真实性校验</th>
            <th class="p-2.5">AI 排表说明</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-zinc-100">
          ${shifts.map(s => `
            <tr class="hover:bg-zinc-50 transition">
              <td class="p-2.5 font-mono font-bold text-black">${s.date}</td>
              <td class="p-2.5 font-medium text-zinc-600">${s.shift_period}</td>
              <td class="p-2.5 font-semibold text-black">${s.building}</td>
              <td class="p-2.5 font-bold text-black flex items-center gap-1">
                <i class="fa-solid fa-user-check text-emerald-600 text-[10px]"></i>
                <span>${s.member_names}</span>
              </td>
              <td class="p-2.5">
                <span class="pill-badge pill-badge-green text-[9px]">100% 真实在册</span>
              </td>
              <td class="p-2.5 text-zinc-500 text-[11px]">${s.remark || '自动均衡'}</td>
            </tr>
          `).join("")}
        </tbody>
      </table>
    `;
  }
}

async function applyAIScheduleToSystem() {
  if (!currentAISuggestedShifts || currentAISuggestedShifts.length === 0) {
    toast("当前没有可落盘的排班班次", "warning");
    return;
  }

  if (!confirm(`确定要将 AI 生成的 ${currentAISuggestedShifts.length} 个排班班次正式发布到学管会系统中吗？`)) {
    return;
  }

  const res = await request("/minister/ai-schedule/apply", {
    method: "POST",
    body: JSON.stringify({
      plan_title: `AI 对话智能交互排班_${new Date().toISOString().slice(0, 10)}`,
      shifts: currentAISuggestedShifts,
    }),
  });

  if (res && res.ok) {
    const data = await res.json();
    toast("🎉 " + data.message, "success");
    // 切换到常规管理大盘刷新班次查看
    switchMinisterSubTab("overview");
  } else if (res) {
    const err = await res.json();
    toast("应用排班失败: " + (err.error || "未知原因"), "error");
  }
}

// 全局缓存当前周履职状态 (供排班表三色高亮使用)
let cachedWeekDutyStatus = {
  completed_members: [],
  unworked_members: [],
  missed_members: []
};

// 加载当前周 (ISO Week) 部员排班履职三色全景大盘
async function loadWeekDutyStatusBoard() {
  const res = await request("/minister/week-duty-status", { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();
  cachedWeekDutyStatus = data;

  const weekBadge = document.getElementById("duty-spectrum-week-badge");
  if (weekBadge && data.current_week_str) {
    weekBadge.innerText = `${data.current_week_str} 全景`;
  }

  // 1. 已值班绿色区域
  const compCountEl = document.getElementById("duty-count-completed");
  const compListEl = document.getElementById("duty-list-completed");
  if (compCountEl) compCountEl.innerText = `${data.completed_count || 0} 人`;
  if (compListEl) {
    if (!data.completed_members || data.completed_members.length === 0) {
      compListEl.innerHTML = `<span class="text-[11px] text-emerald-700/60 py-1">本周暂无已完成值班部员</span>`;
    } else {
      compListEl.innerHTML = data.completed_members.map(m => `
        <span class="duty-tag-completed" title="${m.real_name} (${m.department}) - 本周已完成 ${m.duty_count} 班次">
          <i class="fa-solid fa-circle-check mr-1 text-[10px]"></i>${m.real_name}
          <span class="opacity-70 font-mono text-[9px] ml-1">${m.duty_count}次</span>
        </span>
      `).join("");
    }
  }

  // 2. 未值班蓝色区域
  const unworkedCountEl = document.getElementById("duty-count-unworked");
  const unworkedListEl = document.getElementById("duty-list-unworked");
  if (unworkedCountEl) unworkedCountEl.innerText = `${data.unworked_count || 0} 人`;
  if (unworkedListEl) {
    if (!data.unworked_members || data.unworked_members.length === 0) {
      unworkedListEl.innerHTML = `<span class="text-[11px] text-sky-700/60 py-1">本周全员均已上岗完成</span>`;
    } else {
      unworkedListEl.innerHTML = data.unworked_members.map(m => `
        <span class="duty-tag-unworked" title="${m.real_name} (${m.department}) - 本周待值班 / 尚未排班上岗">
          <i class="fa-solid fa-clock mr-1 text-[10px]"></i>${m.real_name}
        </span>
      `).join("");
    }
  }

  // 3. 旷工缺勤红色区域
  const missedCountEl = document.getElementById("duty-count-missed");
  const missedListEl = document.getElementById("duty-list-missed");
  if (missedCountEl) missedCountEl.innerText = `${data.missed_count || 0} 人`;
  if (missedListEl) {
    if (!data.missed_members || data.missed_members.length === 0) {
      missedListEl.innerHTML = `<span class="text-[11px] text-emerald-700/80 py-1 flex items-center"><i class="fa-solid fa-shield-heart mr-1"></i>本周全员纪律优良，无旷工记录</span>`;
    } else {
      missedListEl.innerHTML = data.missed_members.map(m => `
        <span class="duty-tag-missed" title="${m.real_name} (${m.department}) - 本周缺勤/旷工 ${m.missed_count} 次！">
          <i class="fa-solid fa-triangle-exclamation mr-1 text-[10px]"></i>${m.real_name}
          <span class="opacity-80 font-mono text-[9px] ml-1">旷${m.missed_count}次</span>
        </span>
      `).join("");
    }
  }
}

// 依据三色履职图谱将班次中的部员姓名渲染为不同颜色徽章
function renderColorCodedMemberNames(namesStr) {
  if (!namesStr) return `<span class="text-zinc-400">未指定</span>`;
  const names = namesStr.split(/[,，、\s]+/).filter(Boolean);
  if (names.length === 0) return `<span class="text-zinc-400">未指定</span>`;

  const completedSet = new Set((cachedWeekDutyStatus.completed_members || []).map(m => m.real_name));
  const missedSet = new Set((cachedWeekDutyStatus.missed_members || []).map(m => m.real_name));

  return names.map(name => {
    if (missedSet.has(name)) {
      return `<span class="duty-tag-missed inline-flex items-center mr-1 mb-1 text-[11px]"><i class="fa-solid fa-triangle-exclamation mr-1 text-[9px]"></i>${name} (旷工)</span>`;
    } else if (completedSet.has(name)) {
      return `<span class="duty-tag-completed inline-flex items-center mr-1 mb-1 text-[11px]"><i class="fa-solid fa-check mr-1 text-[9px]"></i>${name} (已值班)</span>`;
    } else {
      return `<span class="duty-tag-unworked inline-flex items-center mr-1 mb-1 text-[11px]"><i class="fa-solid fa-hourglass-start mr-1 text-[9px]"></i>${name} (待值班)</span>`;
    }
  }).join("");
}

function renderShiftStatusBadge(status) {
  if (status === "completed") {
    return `<span class="pill-badge pill-badge-green text-[10px]"><i class="fa-solid fa-check mr-0.5"></i>已履职</span>`;
  } else if (status === "missed") {
    return `<span class="pill-badge text-[10px] bg-rose-100 text-rose-700 border border-rose-200"><i class="fa-solid fa-xmark mr-0.5"></i>已旷工</span>`;
  }
  return `<span class="pill-badge pill-badge-gray text-[10px]">待执行</span>`;
}

// 已结算的班次不再给按钮：核销接口幂等，但重复点击只会拿到 409，提前收口更清楚
function renderShiftSettleButton(shift) {
  if (shift.status === "completed") {
    return `<span class="text-[10px] text-emerald-600 font-mono"><i class="fa-solid fa-plus mr-0.5"></i>已按出勤加分结算</span>`;
  }
  if (shift.status === "missed") {
    return `<span class="text-[10px] text-rose-500 font-mono">已判旷工扣分</span>`;
  }
  return `
    <button onclick="completeShift(${shift.id}, '${escapeAttr(shift.date)}')" class="btn-pill btn-pill-dark text-[10px] py-1 px-3" title="核销后当班部员各 +5 履职积分，不可重复执行">
      <i class="fa-solid fa-clipboard-check mr-1"></i> 核销加分
    </button>`;
}

async function completeShift(shiftID, dateLabel) {
  if (!confirm(`确认核销 ${dateLabel} 的该班次？\n当班部员将各 +5 履职积分，核销后不可重复执行。`)) return;
  const res = await request(`/minister/schedules/${shiftID}/complete`, { method: "POST", body: JSON.stringify({}) });
  if (!res) return;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    toast(data.error || "班次核销失败", "error");
    return;
  }
  toast(data.message || "班次已核销", "success");
  await reloadMinisterShiftBoard();
}

async function sweepMissedShifts() {
  if (!confirm("旷工扫描将处理【今天之前】所有仍未核销的班次：\n· 无故缺勤 → 班次标红，当班部员各 -5 分\n· 已请假的班次 → 正常销班，不扣分\n\n该操作会真实改动积分，确认执行？")) return;
  const res = await request("/minister/schedules/sweep-missed", { method: "POST", body: JSON.stringify({}) });
  if (!res) return;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    toast(data.error || "旷工扫描失败", "error");
    return;
  }
  toast(data.message || "扫描完成", "success");
  await reloadMinisterShiftBoard();
}

// 结算后只需刷新排班大盘与三色履职数据，整页重读会把部长正在输入的筛选条件冲掉
async function reloadMinisterShiftBoard() {
  await loadWeekDutyStatusBoard();
  const sRes = await request("/minister/schedules", { method: "GET" });
  if (!sRes || !sRes.ok) return;
  const data = await sRes.json();
  const statsEl = document.getElementById("minister-shift-stats");
  if (statsEl) statsEl.innerText = `已排 ${data.total} 班次`;
  const wrap = document.getElementById("minister-shift-table-wrap");
  if (wrap) wrap.innerHTML = renderMinisterShiftTable(data.items || []);
}

function renderMinisterShiftTable(items) {
  if (items.length === 0) {
    return `<div class="text-center py-12 text-zinc-400 text-xs">当前没有排班班次</div>`;
  }
  return `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
        <tr>
          <th class="p-2.5">日期</th>
          <th class="p-2.5">轮换周期</th>
          <th class="p-2.5">时段</th>
          <th class="p-2.5">楼栋</th>
          <th class="p-2.5">当值部员 (履职三色标记)</th>
          <th class="p-2.5">协同宿管</th>
          <th class="p-2.5">班次状态</th>
          <th class="p-2.5 text-right">履职核销</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100">
        ${items.map(s => `
          <tr>
            <td class="p-2.5 font-bold font-mono">${s.date}</td>
            <td class="p-2.5"><span class="pill-badge pill-badge-gray text-[10px]">${s.week_type === 'double' ? '双周轮换' : '单周轮换'}</span></td>
            <td class="p-2.5">${s.shift_period}</td>
            <td class="p-2.5 font-bold">${s.building}</td>
            <td class="p-2.5 font-bold">${renderColorCodedMemberNames(s.member_names)}</td>
            <td class="p-2.5 text-zinc-600">${s.manager_name}</td>
            <td class="p-2.5">${renderShiftStatusBadge(s.status)}</td>
            <td class="p-2.5 text-right">${renderShiftSettleButton(s)}</td>
          </tr>
        `).join("")}
      </tbody>
    </table>`;
}

async function loadMinisterPanel() {
  // 1. 待审请假
  const lRes = await request("/minister/leaves?status=pending", { method: "GET" });
  if (lRes && lRes.ok) {
    const data = await lRes.json();
    document.getElementById("minister-pending-badge").innerText = `${data.total} 待审批`;
    const container = document.getElementById("minister-leave-review-list");
    if (data.items.length === 0) {
      container.innerHTML = `<div class="text-xs text-zinc-400 p-3 text-center">暂无待审批的请假申请</div>`;
    } else {
      container.innerHTML = data.items.map(l => {
        let subBadge = l.substitute_name
          ? `<span class="pill-badge pill-badge-dark text-[10px]">替班: ${l.substitute_name}</span>`
          : `<span class="pill-badge pill-badge-gray text-[10px]">未手动指派</span>`;
        if (l.auto_substitute) {
          subBadge += ` <span class="pill-badge pill-badge-green text-[10px] ml-1"><i class="fa-solid fa-wand-magic-sparkles mr-0.5"></i> 算法自动替补</span>`;
        }
        return `
          <div class="p-4 rounded-2xl bg-zinc-50 border border-zinc-200 flex flex-col sm:flex-row sm:items-center justify-between gap-3 text-xs">
            <div class="space-y-1">
              <div class="flex items-center space-x-2">
                <span class="font-bold text-black text-sm">部员【${l.member_name}】请假申请</span>
                ${subBadge}
              </div>
              <div class="text-zinc-500">班次: <span class="font-medium text-black">${l.shift_info}</span> · 事由: ${l.reason}</div>
              ${l.substitute_reason ? `<div class="text-[11px] text-emerald-600 font-medium"><i class="fa-solid fa-robot mr-1"></i> ${l.substitute_reason}</div>` : ''}
            </div>
            <div class="flex items-center gap-2 flex-shrink-0">
              <button onclick="reviewMinisterLeave(${l.id}, 'approved', true)" class="btn-pill btn-pill-dark text-xs py-1.5 px-3.5 shadow-sm" title="批准请假并确认由系统算法自动指派最近上工最少人员替补">
                <i class="fa-solid fa-check mr-1"></i> 批准并算法替补
              </button>
              <button onclick="reviewMinisterLeave(${l.id}, 'approved', false)" class="btn-pill btn-pill-light text-xs py-1.5 px-3">
                仅准假
              </button>
              <button onclick="reviewMinisterLeave(${l.id}, 'rejected', false)" class="btn-pill btn-pill-light text-xs py-1.5 px-3 text-red-500 hover:bg-red-50">
                驳回
              </button>
            </div>
          </div>
        `;
      }).join("");
    }
  }

  // 2. 本周部员三色履职大盘数据加载与排班表格三色姓名渲染
  await loadWeekDutyStatusBoard();

  const sRes = await request("/minister/schedules", { method: "GET" });
  if (sRes && sRes.ok) {
    const data = await sRes.json();
    document.getElementById("minister-shift-stats").innerText = `已排 ${data.total} 班次`;
    const wrap = document.getElementById("minister-shift-table-wrap");
    wrap.innerHTML = renderMinisterShiftTable(data.items || []);
  }

  // 3. 本部全员积分与最优上工、最高积分标兵
  const mRes = await request("/minister/members", { method: "GET" });
  if (mRes && mRes.ok) {
    const data = await mRes.json();
    
    // 渲染部门范围与总览统计
    const scopeEl = document.getElementById("minister-dept-scope-title");
    if (scopeEl) scopeEl.innerText = data.department_scope || "本部";
    const tagEl = document.getElementById("minister-dept-stats-tag");
    if (tagEl) tagEl.innerText = `共 ${data.total_members} 人 · 部门均分 ${data.department_avg_score} 分`;

    // 标兵公示不再由这里渲染：/minister/members 的 top_score_member、best_duty_member
    // 是实时台账榜首，每天随调分变化，不能当作公示结果。公示卡片改由 loadWeeklyHonorBoard
    // 从评定快照读取。

    // 渲染全员详细台账大表
    const wrap = document.getElementById("minister-member-table-wrap");
    if (wrap) {
      wrap.innerHTML = `
        <table class="w-full text-xs text-left">
          <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
            <tr>
              <th class="p-2.5">部员姓名</th>
              <th class="p-2.5">所属组别</th>
              <th class="p-2.5">手机号</th>
              <th class="p-2.5">考核总积分</th>
              <th class="p-2.5">上工出勤</th>
              <th class="p-2.5">请假次数</th>
              <th class="p-2.5">缺工记录</th>
              <th class="p-2.5">考评档次</th>
              <th class="p-2.5 text-right">调分操作</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-zinc-100">
            ${data.items.map(m => `
              <tr class="hover:bg-zinc-50 transition">
                <td class="p-2.5 font-bold text-black">${m.real_name}</td>
                <td class="p-2.5 text-zinc-600">${m.department}</td>
                <td class="p-2.5 font-mono text-zinc-400">${m.phone}</td>
                <td class="p-2.5 font-mono font-black text-black text-sm">${m.total_score} 分</td>
                <td class="p-2.5 font-mono font-semibold text-black">${m.duty_count} 班次</td>
                <td class="p-2.5 font-mono text-zinc-500">${m.leave_count} 次</td>
                <td class="p-2.5 font-mono ${m.missed_count > 0 ? 'text-black font-bold' : 'text-emerald-600 font-medium'}">${m.missed_count > 0 ? m.missed_count + ' 次' : '全勤'}</td>
                <td class="p-2.5"><span class="pill-badge ${m.total_score >= 110 ? 'pill-badge-dark' : 'pill-badge-gray'} text-[10px]">${m.honor_badge}</span></td>
                <td class="p-2.5 text-right">
                  <button onclick="quickScorePrompt(${m.id}, '${m.real_name}')" class="btn-pill btn-pill-light text-xs py-1 px-2.5">
                    调整积分
                  </button>
                </td>
              </tr>
            `).join("")}
          </tbody>
        </table>
      `;
    }
  }

  // 4. 每周标兵公示：读评定快照与留痕
  await loadWeeklyHonorBoard();
}

const HONOR_RANK_LABELS = { top_score: "最高积分", best_duty: "最优上工" };

function setHonorText(id, value) {
  const el = document.getElementById(id);
  if (el) el.textContent = value === null || value === undefined || value === "" ? "—" : String(value);
}

function formatHonorTime(raw) {
  if (!raw) return "";
  const d = new Date(raw);
  return isNaN(d.getTime()) ? String(raw) : d.toLocaleString("zh-CN", { hour12: false });
}

// 每周标兵公示：卡片只读评定快照。未评定时如实留空，
// 不拿实时台账榜首冒充公示结果——那正是公示不可复核的根源。
async function loadWeeklyHonorBoard() {
  const res = await request("/minister/honors/weekly", { method: "GET" });
  const data = res && res.ok ? await res.json() : null;
  const scope = (data && data.scope) || (state.user && state.user.department) || "本部";
  setHonorText("honor-scope-label", scope);

  const evaluated = !!(data && data.evaluated);
  const emptyBox = document.getElementById("honor-empty-box");
  if (emptyBox) emptyBox.classList.toggle("hidden", evaluated);

  if (!evaluated) {
    setHonorText("honor-week-pill", (data && data.current_week) || "未读取到");
    setHonorText("honor-evaluate-meta", (data && data.note) || "公示读取失败，请稍后重试或联系技术维护组");
    ["top-score", "best-duty"].forEach((key) => {
      setHonorText(`minister-${key}-name`, "待评定");
      setHonorText(`minister-${key}-val`, "—");
      setHonorText(`minister-${key}-dept`, scope);
      setHonorText(`minister-${key}-note`, "评定后生成本周快照");
    });
    setHonorText("minister-top-score-badge", "待评定");
    setHonorText("minister-top-score-extra", "—");
    setHonorText("minister-best-duty-pill", "待评定");
    setHonorText("minister-best-duty-missed", "—");
    setHonorText("minister-best-duty-extra", "—");
    await loadHonorHistory();
    return;
  }

  setHonorText("honor-week-pill", data.stale ? `${data.week} · 非本周` : data.week);

  const byRank = {};
  (data.items || []).forEach((item) => { byRank[item.rank_type] = item; });

  const top = byRank.top_score;
  if (top) {
    setHonorText("minister-top-score-name", top.member_name);
    setHonorText("minister-top-score-val", top.total_score);
    setHonorText("minister-top-score-dept", `${top.department || scope} · 部员 ID ${top.member_id}`);
    setHonorText("minister-top-score-badge", top.badge);
    setHonorText("minister-top-score-note", top.note);
    setHonorText("minister-top-score-extra", `上岗 ${top.duty_count} 班 · 缺工 ${top.missed_count} 次`);
  }

  const duty = byRank.best_duty;
  if (duty) {
    setHonorText("minister-best-duty-name", duty.member_name);
    setHonorText("minister-best-duty-val", duty.duty_count);
    setHonorText("minister-best-duty-dept", `${duty.department || scope} · 积分 ${duty.total_score}`);
    setHonorText("minister-best-duty-pill", duty.missed_count === 0 ? "本周零缺工" : `缺工 ${duty.missed_count} 次`);
    setHonorText("minister-best-duty-note", duty.note);
    setHonorText("minister-best-duty-missed", `${duty.missed_count} 次`);
    setHonorText("minister-best-duty-extra", duty.badge);
  }

  const anchor = top || duty;
  const meta = [];
  if (anchor) {
    meta.push(`评定人【${anchor.evaluated_by || "未知"}】`);
    const at = formatHonorTime(anchor.evaluated_at);
    if (at) meta.push(`评定于 ${at}`);
  }
  meta.push("结果已存档，后续调分不会改写本档");
  if (data.stale) meta.push(`本周（${data.current_week}）尚未评定`);
  setHonorText("honor-evaluate-meta", meta.join(" · "));

  await loadHonorHistory();
}

async function loadHonorHistory() {
  const wrap = document.getElementById("minister-honor-history");
  if (!wrap) return;
  const res = await request("/minister/honors/history?limit=4", { method: "GET" });
  if (!res || !res.ok) {
    wrap.innerHTML = "";
    return;
  }
  const data = await res.json();
  const weeks = (data.weeks || []).filter((w) => w.items && w.items.length > 1);
  if (!weeks.length) {
    wrap.innerHTML = "";
    return;
  }
  wrap.innerHTML = `
    <div class="rounded-2xl border border-zinc-200 bg-white p-4 space-y-3">
      <div class="flex items-center justify-between">
        <h4 class="font-bold text-black text-xs"><i class="fa-solid fa-clock-rotate-left mr-2"></i>公示留痕（最近 ${weeks.length} 期）</h4>
        <span class="text-[10px] text-zinc-400">每评定期一档，可回溯</span>
      </div>
      ${weeks.map((w) => `
        <div class="space-y-1.5">
          <div class="font-mono text-[11px] text-zinc-500">${escapeHtml(w.week)}</div>
          <div class="flex flex-wrap gap-2">
            ${w.items.map((it) => `
              <span class="pill-badge pill-badge-gray text-[10px]">
                ${escapeHtml(HONOR_RANK_LABELS[it.rank_type] || it.rank_type)}：${escapeHtml(it.member_name)}
                · ${escapeHtml(String(it.total_score))} 分 · 上岗 ${escapeHtml(String(it.duty_count))} 班
              </span>`).join("")}
          </div>
        </div>`).join("")}
    </div>`;
}

async function evaluateWeeklyHonor() {
  const scope = document.getElementById("honor-scope-label")?.textContent || "本部";
  const confirmed = confirm(
    `确认评定【${scope}】本周标兵并生成公示快照？\n` +
    "同一周重复评定会覆盖本周已公示的结果，历史周次不受影响。"
  );
  if (!confirmed) return;

  const res = await request("/minister/honors/evaluate", { method: "POST" });
  if (res && res.ok) {
    const data = await res.json();
    toast(data.message || "本周标兵已评定并公示", "success");
    loadWeeklyHonorBoard();
    return;
  }
  if (res) {
    const err = await res.json().catch(() => ({}));
    toast(err.error || "标兵评定失败，请稍后重试", "error");
  }
}

async function reviewMinisterLeave(id, action, autoSub = true) {
  const defaultComment = action === 'approved'
    ? (autoSub ? '同意准假，并由系统算法自动匹配指派近期上工最少部员接替' : '同意准假')
    : '人手调配不均，请先协调妥善后再报';
  const comment = prompt(`请输入审批意见（${action === 'approved' ? '批准' : '驳回'}）：`, defaultComment);
  if (comment === null) return;
  const res = await request(`/minister/leaves/${id}/review`, {
    method: "POST",
    body: JSON.stringify({ action, comment, auto_substitute: autoSub }),
  });
  if (res && res.ok) {
    const data = await res.json();
    let msg = data.message || "审批已执行完成！";
    if (data.substitute) {
      msg += `\n【智能替补】：由【${data.substitute}】接替排班上岗并自动加分`;
    }
    toast(msg, "info");
    loadMinisterPanel();
  }
}

function openScheduleGenModal() {
  const t = new Date().toISOString().slice(0, 10);
  const n = new Date(Date.now() + 30 * 86400000).toISOString().slice(0, 10);
  document.getElementById("inp-gen-start").value = t;
  document.getElementById("inp-gen-end").value = n;
  document.getElementById("modal-sched-gen").classList.remove("hidden");
}
function closeScheduleGenModal() {
  document.getElementById("modal-sched-gen").classList.add("hidden");
}
async function handleScheduleGenSubmit(e) {
  e.preventDefault();
  const res = await request("/minister/schedule-plans", {
    method: "POST",
    body: JSON.stringify({
      title: document.getElementById("inp-gen-title").value.trim(),
      rule_type: document.getElementById("inp-gen-rule").value,
      shift_period: document.getElementById("inp-gen-period").value,
      start_date: document.getElementById("inp-gen-start").value,
      end_date: document.getElementById("inp-gen-end").value,
      buildings: ["东区7号楼", "西区12号楼", "南区3号楼"],
    }),
  });
  if (res && res.ok) {
    toast("排班轮换生成完成！", "success");
    closeScheduleGenModal();
    loadMinisterPanel();
  }
}

let ministerScorePolicy = null;

async function getMinisterScorePolicy() {
  if (ministerScorePolicy) return ministerScorePolicy;
  const res = await request("/minister/score-policy", { method: "GET" });
  if (!res || !res.ok) return null;
  ministerScorePolicy = await res.json();
  return ministerScorePolicy;
}

async function quickScorePrompt(id, name) {
  const policy = await getMinisterScorePolicy();
  const limitHint = policy
    ? `（校级策略：单次不超过 ${policy.manual_max_single} 分，同一部员每 7 天累计 ${policy.manual_weekly_quota} 分，达 ${policy.manual_review_at} 分将进入技术维护组复核）`
    : "（正数加分，负数扣分）";
  const valStr = prompt(`请输入为部员【${name}】调整的分值 ${limitHint}`, "+5");
  if (!valStr) return;
  const change = parseInt(valStr);
  if (isNaN(change) || change === 0) {
    toast("请输入非零的整数分值", "warning");
    return;
  }
  if (policy && Math.abs(change) > policy.manual_max_single) {
    toast(`单次灵活调分不得超过 ${policy.manual_max_single} 分`, "warning");
    return;
  }

  const reason = prompt("请输入增减分缘由（4 ~ 120 字，将随流水长期留痕并接受复核）：", "查寝规范履职表现突出");
  if (reason === null) return;
  const reasonText = reason.trim();
  if (reasonText.length < 4 || reasonText.length > 120) {
    toast("调整事由必须填写，长度 4 ~ 120 字", "warning");
    return;
  }
  const confirmPwd = askStepUp("调整他人积分为高危操作，请输入当前登录口令二次确认：");
  if (!confirmPwd) return;

  const res = await request("/minister/scores/adjust", {
    method: "POST",
    headers: { "X-Confirm-Password": confirmPwd },
    body: JSON.stringify({
      member_id: id,
      score_change: change,
      reason: reasonText,
    }),
  });
  if (res && res.ok) {
    const data = await res.json().catch(() => ({}));
    toast(data.message || "积分更新完成！", "success");
    if (currentUser && currentUser.role === "tech_admin") {
      loadTechMembersOverview(currentTechDeptFilter);
    } else {
      loadMinisterPanel();
    }
  } else if (res) {
    const data = await res.json().catch(() => ({}));
    toast(data.error || "调分失败", "error");
  }
}

// =============================================================================
// 6. 技术组底层运维控制台 (角色: tech_admin)
// =============================================================================
let currentTechDeptFilter = "";

async function loadTechPanel() {
  switchTechSubTab("db");
  loadTechRosterTable();
  loadTechMembersOverview("");
}

function filterTechDepartment(dept) {
  currentTechDeptFilter = dept;
  const btnAll = document.getElementById("btn-tech-dept-all");
  const btnJj = document.getElementById("btn-tech-dept-jj");
  const btnZz = document.getElementById("btn-tech-dept-zz");
  const btnXc = document.getElementById("btn-tech-dept-xc");
  
  if (btnAll) btnAll.className = `nav-pill-item ${dept === '' ? 'active' : ''}`;
  if (btnJj) btnJj.className = `nav-pill-item ${dept === '纪检部' ? 'active' : ''}`;
  if (btnZz) btnZz.className = `nav-pill-item ${dept === '组织部' ? 'active' : ''}`;
  if (btnXc) btnXc.className = `nav-pill-item ${dept === '宣传部' ? 'active' : ''}`;

  loadTechMembersOverview(dept);
}

async function loadTechMembersOverview(dept = "") {
  const query = dept ? `?department=${encodeURIComponent(dept)}` : "";
  const res = await request(`/minister/members${query}`, { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();

  // 1. 全校/所选部门最高积分标兵
  const topScoreName = document.getElementById("tech-top-score-name");
  const topScoreDept = document.getElementById("tech-top-score-dept");
  const topScoreVal = document.getElementById("tech-top-score-val");
  if (topScoreName && topScoreVal) {
    if (data.top_score_member) {
      topScoreName.innerText = data.top_score_member.real_name;
      topScoreDept.innerText = `${data.top_score_member.department} · 评定: ${data.top_score_member.honor_badge}`;
      topScoreVal.innerText = data.top_score_member.total_score;
    } else {
      topScoreName.innerText = "暂无数据";
      topScoreDept.innerText = "-";
      topScoreVal.innerText = "0";
    }
  }

  // 2. 全校/所选部门最优上工标兵
  const bestDutyName = document.getElementById("tech-best-duty-name");
  const bestDutyDept = document.getElementById("tech-best-duty-dept");
  const bestDutyVal = document.getElementById("tech-best-duty-val");
  if (bestDutyName && bestDutyVal) {
    if (data.best_duty_member) {
      bestDutyName.innerText = data.best_duty_member.real_name;
      bestDutyDept.innerText = `${data.best_duty_member.department} · 缺勤 ${data.best_duty_member.missed_count} 次`;
      bestDutyVal.innerText = data.best_duty_member.duty_count;
    } else {
      bestDutyName.innerText = "暂无数据";
      bestDutyDept.innerText = "-";
      bestDutyVal.innerText = "0";
    }
  }

  // 3. 全员明细台账大表
  const wrap = document.getElementById("tech-all-members-table-wrap");
  if (wrap) {
    if (!data.items || data.items.length === 0) {
      wrap.innerHTML = `<div class="text-center py-8 text-zinc-400 text-xs">当前筛选部门暂无部员档案</div>`;
      return;
    }
    wrap.innerHTML = `
      <table class="w-full text-xs text-left">
        <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
          <tr>
            <th class="p-2.5">部员姓名</th>
            <th class="p-2.5">所属部门与组别</th>
            <th class="p-2.5">负责楼栋</th>
            <th class="p-2.5">手机号码</th>
            <th class="p-2.5">综合履职总积分</th>
            <th class="p-2.5">查寝出勤班次</th>
            <th class="p-2.5">请假审批</th>
            <th class="p-2.5">缺工违纪</th>
            <th class="p-2.5">考评荣誉称号</th>
            <th class="p-2.5 text-right">技术运维调权</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-zinc-100">
          ${data.items.map(m => `
            <tr class="hover:bg-zinc-50 transition">
              <td class="p-2.5 font-bold text-black">${m.real_name}</td>
              <td class="p-2.5 font-medium text-zinc-700">${m.department}</td>
              <td class="p-2.5 text-zinc-500">${m.building || '待分配'}</td>
              <td class="p-2.5 font-mono text-zinc-400">${m.phone}</td>
              <td class="p-2.5 font-mono font-black text-black text-sm">${m.total_score} 分</td>
              <td class="p-2.5 font-mono font-bold text-black">${m.duty_count} 班次</td>
              <td class="p-2.5 font-mono text-zinc-500">${m.leave_count} 次</td>
              <td class="p-2.5 font-mono ${m.missed_count > 0 ? 'text-black font-bold' : 'text-emerald-600 font-medium'}">${m.missed_count > 0 ? m.missed_count + ' 次' : '全勤模范'}</td>
              <td class="p-2.5"><span class="pill-badge ${m.total_score >= 110 ? 'pill-badge-dark' : 'pill-badge-gray'} text-[10px]">${m.honor_badge}</span></td>
              <td class="p-2.5 text-right">
                <button onclick="quickScorePrompt(${m.id}, '${m.real_name}')" class="btn-pill btn-pill-light text-xs py-1 px-2.5">
                  调分
                </button>
              </td>
            </tr>
          `).join("")}
        </tbody>
      </table>
    `;
  }
}

async function loadTechRosterTable() {
  const res = await request("/tech/roster-presets", { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();
  const wrap = document.getElementById("tech-roster-table-wrap");
  wrap.innerHTML = `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
        <tr>
          <th class="p-2.5">姓名</th>
          <th class="p-2.5">预置手机号 (三要素免密源)</th>
          <th class="p-2.5">负责楼栋</th>
          <th class="p-2.5">移动端激活状态</th>
          <th class="p-2.5">录入时间</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100">
        ${data.items.map(r => `
          <tr>
            <td class="p-2.5 font-bold text-black">${r.real_name}</td>
            <td class="p-2.5 font-mono text-zinc-800 font-bold">${r.phone}</td>
            <td class="p-2.5">${r.building}</td>
            <td class="p-2.5"><span class="pill-badge ${r.is_activated ? 'pill-badge-green' : 'pill-badge-gray'} text-[10px]">${r.is_activated ? '已激活' : '待首次直登'}</span></td>
            <td class="p-2.5 text-zinc-400 font-mono text-[10px]">${r.created_at.slice(0, 10)}</td>
          </tr>
        `).join("")}
      </tbody>
    </table>
  `;
}

function openAddRosterModal() {
  document.getElementById("modal-roster-add").classList.remove("hidden");
}
function closeAddRosterModal() {
  document.getElementById("modal-roster-add").classList.add("hidden");
}
async function handleAddRosterSubmit(e) {
  e.preventDefault();
  const res = await request("/tech/roster-presets", {
    method: "POST",
    body: JSON.stringify({
      real_name: document.getElementById("inp-rst-name").value.trim(),
      phone: document.getElementById("inp-rst-phone").value.trim(),
      building: document.getElementById("inp-rst-bldg").value.trim(),
      floor: "全楼",
    }),
  });
  if (res && res.ok) {
    toast("宿管预置录入成功，宿管可立即在手机端三要素直登！", "success");
    closeAddRosterModal();
    loadTechRosterTable();
  }
}

// =============================================================================
// 6.2 技术维护组：底层后台数据库与各名单管理 (左侧分栏选择数据库，右侧管理数据)
// =============================================================================
let currentTechDBTables = [];
let currentActiveDBTableKey = "students";
let currentActiveDBTableMeta = null;
let currentDBTableRecords = [];
let currentDBPage = 1;
let currentDBPageSize = 15;
let currentDBTotalPages = 1;
let currentDBSearchQuery = "";
let currentEditingRecordId = null;
let dbSearchDebounceTimer = null;

function switchTechSubTab(tab) {
  const btnDb = document.getElementById("btn-tech-sub-db");
  const btnSlots = document.getElementById("btn-tech-sub-slots");
  const btnAi = document.getElementById("btn-tech-sub-ai");
  const btnScore = document.getElementById("btn-tech-sub-score");
  const viewDb = document.getElementById("tech-subview-db");
  const viewSlots = document.getElementById("tech-subview-slots");
  const viewAi = document.getElementById("tech-subview-ai");
  const viewScore = document.getElementById("tech-subview-score");

  if (tab === "db") {
    if (btnDb) btnDb.className = "nav-pill-item active";
    if (btnSlots) btnSlots.className = "nav-pill-item";
    if (btnAi) btnAi.className = "nav-pill-item";
    if (btnScore) btnScore.className = "nav-pill-item";
    if (viewDb) viewDb.classList.remove("hidden");
    if (viewSlots) viewSlots.classList.add("hidden");
    if (viewAi) viewAi.classList.add("hidden");
    if (viewScore) viewScore.classList.add("hidden");
    loadTechDBTables();
  } else if (tab === "slots") {
    if (btnDb) btnDb.className = "nav-pill-item";
    if (btnSlots) btnSlots.className = "nav-pill-item active";
    if (btnAi) btnAi.className = "nav-pill-item";
    if (btnScore) btnScore.className = "nav-pill-item";
    if (viewDb) viewDb.classList.add("hidden");
    if (viewSlots) viewSlots.classList.remove("hidden");
    if (viewAi) viewAi.classList.add("hidden");
    if (viewScore) viewScore.classList.add("hidden");
    loadTechSlotConfigs();
  } else if (tab === "ai") {
    if (btnDb) btnDb.className = "nav-pill-item";
    if (btnSlots) btnSlots.className = "nav-pill-item";
    if (btnScore) btnScore.className = "nav-pill-item";
    if (btnAi) btnAi.className = "nav-pill-item active";
    if (viewDb) viewDb.classList.add("hidden");
    if (viewSlots) viewSlots.classList.add("hidden");
    if (viewScore) viewScore.classList.add("hidden");
    if (viewAi) viewAi.classList.remove("hidden");
    loadAIEngineConfigs();
  } else {
    if (btnDb) btnDb.className = "nav-pill-item";
    if (btnSlots) btnSlots.className = "nav-pill-item";
    if (btnAi) btnAi.className = "nav-pill-item";
    if (btnScore) btnScore.className = "nav-pill-item active";
    if (viewDb) viewDb.classList.add("hidden");
    if (viewSlots) viewSlots.classList.add("hidden");
    if (viewAi) viewAi.classList.add("hidden");
    if (viewScore) viewScore.classList.remove("hidden");
    loadScorePolicyPanel();
  }
}

// -----------------------------------------------------------------------------
// 校级积分策略与部长灵活调分复核（技术维护组）
// -----------------------------------------------------------------------------
let currentScorePolicy = null;
let scoreAdjustItems = [];

function setInputValue(id, value) {
  const el = document.getElementById(id);
  if (el) el.value = value;
}

function askStepUp(hint) {
  const pwd = prompt(hint);
  if (pwd === null) return null;
  const trimmed = pwd.trim();
  if (!trimmed) {
    toast("口令不能为空，操作已取消", "warning");
    return null;
  }
  return trimmed;
}

async function loadScorePolicyPanel() {
  await loadScorePolicyForm();
  await loadScoreAdjustments();
}

async function loadScorePolicyForm() {
  const meta = document.getElementById("score-policy-meta");
  const res = await request("/minister/score-policy", { method: "GET" });
  if (!res || !res.ok) {
    if (meta) meta.innerText = "积分策略读取失败，请确认当前账号具备部长或技术维护组权限。";
    return;
  }
  currentScorePolicy = await res.json();
  setInputValue("score-inp-attendance", currentScorePolicy.attendance_bonus);
  setInputValue("score-inp-missed", currentScorePolicy.missed_penalty);
  setInputValue("score-inp-max-single", currentScorePolicy.manual_max_single);
  setInputValue("score-inp-weekly-quota", currentScorePolicy.manual_weekly_quota);
  setInputValue("score-inp-review-at", currentScorePolicy.manual_review_at);

  if (meta) {
    meta.innerHTML = currentScorePolicy.updated_by
      ? `最近由 <b>${escapeHtml(currentScorePolicy.updated_by)}</b> 于 ${escapeHtml(formatRecruitTime(currentScorePolicy.updated_at))} 修改。`
      : "当前生效的是系统默认策略（尚无人修改过）。修改并保存后会写入校级策略表，对全校立即生效。";
  }
}

async function saveScorePolicy() {
  const fields = {
    attendance_bonus: "score-inp-attendance",
    missed_penalty: "score-inp-missed",
    manual_max_single: "score-inp-max-single",
    manual_weekly_quota: "score-inp-weekly-quota",
    manual_review_at: "score-inp-review-at",
  };
  const policy = {};
  for (const [key, id] of Object.entries(fields)) {
    const num = parseInt(inputVal(id), 10);
    if (isNaN(num) || num < 0) {
      toast("策略分值必须是非负整数", "warning");
      return;
    }
    policy[key] = num;
  }
  if (policy.manual_weekly_quota < policy.manual_max_single) {
    toast("七日累计额度不能小于单次调分上限", "warning");
    return;
  }

  const pwd = askStepUp("修改校级积分策略会影响全校加分与调分上限，请输入当前登录口令二次确认：");
  if (!pwd) return;

  const res = await request("/minister/score-policy", {
    method: "PUT",
    headers: { "X-Confirm-Password": pwd },
    body: JSON.stringify(policy),
  });
  if (res && res.ok) {
    toast("积分策略已更新，立即对全校生效", "success");
    await loadScorePolicyPanel();
  } else if (res) {
    const data = await res.json().catch(() => ({}));
    toast(data.error || "策略保存失败", "error");
  }
}

async function loadScoreAdjustments() {
  const wrap = document.getElementById("score-adjust-table-wrap");
  if (!wrap) return;
  const days = inputVal("score-filter-days") || "30";
  const dept = inputVal("score-filter-dept");
  const reviewBox = document.getElementById("score-filter-review");
  const reviewOnly = reviewBox && reviewBox.checked;

  const res = await request(
    `/minister/score-adjustments?days=${encodeURIComponent(days)}&department=${encodeURIComponent(dept)}${reviewOnly ? "&review_only=1" : ""}`,
    { method: "GET" }
  );
  if (!res || !res.ok) {
    wrap.innerHTML = `<div class="text-center py-10 text-zinc-400 text-xs">调分清单读取失败（仅技术维护组可查看）</div>`;
    return;
  }
  const data = await res.json();
  scoreAdjustItems = data.items || [];
  const summary = document.getElementById("score-adjust-summary");
  if (summary) summary.innerText = `${data.total} 笔 · 达复核线 ${data.review_count} 笔`;
  renderScoreAdjustTable(scoreAdjustItems);
}

function renderScoreAdjustTable(items) {
  const wrap = document.getElementById("score-adjust-table-wrap");
  if (!wrap) return;
  if (items.length === 0) {
    wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs">该时间范围内没有灵活调分记录</div>`;
    return;
  }

  wrap.innerHTML = `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
        <tr>
          <th class="p-2.5">流水</th>
          <th class="p-2.5">部员 / 部门</th>
          <th class="p-2.5">调分</th>
          <th class="p-2.5">调分后</th>
          <th class="p-2.5">发起部长</th>
          <th class="p-2.5">理由</th>
          <th class="p-2.5">时间</th>
          <th class="p-2.5">状态</th>
          <th class="p-2.5 text-right">操作</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100">
        ${items.map(i => `
          <tr class="hover:bg-zinc-50 transition">
            <td class="p-2.5 font-mono text-zinc-400">#${i.id}</td>
            <td class="p-2.5 font-bold text-black">${escapeHtml(i.member_name)}
              <span class="block font-normal text-zinc-400">${escapeHtml(i.department || "未分部门")}</span>
            </td>
            <td class="p-2.5 font-mono font-bold ${i.score_change >= 0 ? "text-emerald-600" : "text-red-600"}">${i.score_change >= 0 ? "+" : ""}${i.score_change}</td>
            <td class="p-2.5 font-mono text-zinc-600">${i.balance_after}</td>
            <td class="p-2.5 text-zinc-700">${escapeHtml(i.operator_name)}</td>
            <td class="p-2.5 text-zinc-700 max-w-xs truncate" title="${escapeAttr(i.reason)}">${escapeHtml(i.reason)}</td>
            <td class="p-2.5 text-zinc-400 whitespace-nowrap">${escapeHtml(formatRecruitTime(i.created_at))}</td>
            <td class="p-2.5">
              ${i.reversed
                ? `<span class="pill-badge pill-badge-gray text-[10px]">已冲正</span>`
                : i.needs_review
                  ? `<span class="pill-badge pill-badge-amber text-[10px]">待复核</span>`
                  : `<span class="pill-badge pill-badge-green text-[10px]">额度内</span>`}
            </td>
            <td class="p-2.5 text-right">
              ${i.reversed
                ? `<span class="text-[10px] text-zinc-400">反向流水 #${i.reversal_id}</span>`
                : `<button onclick="reverseAdjustment(${i.id})" class="btn-pill btn-pill-light text-xs py-1 px-2.5 text-red-600 hover:border-red-500 font-semibold">冲正</button>`}
            </td>
          </tr>
        `).join("")}
      </tbody>
    </table>
  `;
}

async function reverseAdjustment(logId) {
  const row = scoreAdjustItems.find(i => i.id === logId);
  if (!row) {
    toast("该笔调分记录已不在当前清单中，请刷新后重试", "warning");
    return;
  }
  const reason = prompt(`冲正【${row.member_name}】的这笔 ${row.score_change >= 0 ? "+" : ""}${row.score_change} 分调分：请输入认定它不成立的理由（不少于 4 字，将随流水长期留痕）：`);
  if (reason === null) return;
  if (reason.trim().length < 4) {
    toast("冲正必须填写不少于 4 字的理由", "warning");
    return;
  }
  const pwd = askStepUp("冲正将直接退回积分，请输入当前登录口令二次确认：");
  if (!pwd) return;

  const res = await request(`/minister/score-logs/${logId}/reverse`, {
    method: "POST",
    headers: { "X-Confirm-Password": pwd },
    body: JSON.stringify({ reason: reason.trim() }),
  });
  if (res && res.ok) {
    const data = await res.json().catch(() => ({}));
    toast(data.message || "冲正完成", "success");
    await loadScoreAdjustments();
  } else if (res) {
    const data = await res.json().catch(() => ({}));
    toast(data.error || "冲正失败", "error");
  }
}

// -----------------------------------------------------------------------------
// AI 引擎配置与调度中枢（视觉 + 文本双引擎，ai_status=real 需两者都配置可用）
// -----------------------------------------------------------------------------
function inputVal(id) {
  const el = document.getElementById(id);
  return el ? el.value.trim() : "";
}

let aiEngineConfigs = [];

async function loadAIEngineConfigs() {
  const box = document.getElementById("ai-engine-configs");
  if (!box) return;
  const res = await request("/tech/ai-configs", { method: "GET" });
  if (!res || !res.ok) {
    box.innerHTML = `<div class="col-span-full text-xs text-zinc-400 py-8 text-center">AI 引擎配置加载失败（需要技术维护组权限）</div>`;
    return;
  }
  const data = await res.json();
  aiEngineConfigs = data.items || [];
  if (aiEngineConfigs.length === 0) {
    box.innerHTML = `<div class="col-span-full text-xs text-zinc-400 py-8 text-center">数据库中尚无引擎配置</div>`;
    return;
  }
  box.innerHTML = aiEngineConfigs.map(aiEngineCard).join("");
}

// 可用与否由后端 pkg/ai.IsConfigured 判定并随配置下发：
// 浏览器已经拿不到密钥本身，前端也就无从（更不该）自己拼这条口径。
function aiEngineConfigured(c) {
  return !!c.configured;
}

function aiEngineCard(c) {
  const isVision = c.config_key === "vision_engine";
  const ok = aiEngineConfigured(c);
  return `
  <div id="ai-engine-card-${c.id}" class="p-4 rounded-2xl border border-zinc-200/80 bg-white space-y-2.5">
    <div class="flex items-center justify-between">
      <span class="pill-badge pill-badge-dark text-[10px] font-bold"><i class="fa-solid ${isVision ? 'fa-camera-retro' : 'fa-list-check'} mr-1"></i>${isVision ? "视觉引擎" : "文本引擎"} · ${c.config_key}</span>
      <span class="pill-badge ${ok ? 'pill-badge-green' : 'pill-badge-amber'} text-[9px]">${ok ? "已配置可用" : "未配置完整"}</span>
    </div>
    <input id="ai-name-${c.id}" value="${escapeAttr(c.display_name)}" class="w-full px-3 py-2 rounded-xl border border-zinc-200 text-xs" placeholder="显示名称">
    <input id="ai-endpoint-${c.id}" value="${escapeAttr(c.endpoint)}" class="w-full px-3 py-2 rounded-xl border border-zinc-200 text-[11px] font-mono" placeholder="完整的 /chat/completions 地址（原样使用，不会自动补 /v1）">
    <input id="ai-key-${c.id}" type="password" autocomplete="new-password" class="w-full px-3 py-2 rounded-xl border border-zinc-200 text-[11px] font-mono" placeholder="${c.has_key ? `已存密钥 ${escapeAttr(c.api_key_mask || "")}：留空 = 保留原值` : "API Key"}">
    <div class="grid grid-cols-3 gap-2">
      <input id="ai-model-${c.id}" value="${escapeAttr(c.model_name)}" class="px-3 py-2 rounded-xl border border-zinc-200 text-xs" placeholder="模型名">
      <input id="ai-temp-${c.id}" type="number" step="0.1" min="0" max="2" value="${c.temperature}" class="px-3 py-2 rounded-xl border border-zinc-200 text-xs" title="temperature">
      <input id="ai-tokens-${c.id}" type="number" min="64" step="64" value="${c.max_tokens}" class="px-3 py-2 rounded-xl border border-zinc-200 text-xs" title="max_tokens">
    </div>
    <textarea id="ai-prompt-${c.id}" rows="3" class="w-full px-3 py-2 rounded-xl border border-zinc-200 text-[11px] font-mono" placeholder="System Prompt">${escapeHtml(c.system_prompt || "")}</textarea>
    <label class="flex items-center gap-1.5 text-[11px] text-zinc-600 cursor-pointer">
      <input id="ai-enabled-${c.id}" type="checkbox" ${c.is_enabled ? "checked" : ""} class="accent-black"> 启用该引擎
    </label>
    <button onclick="saveAIEngineConfig(${c.id})" class="btn-pill btn-pill-dark text-xs py-1.5 px-3 w-full">
      <i class="fa-solid fa-floppy-disk mr-1"></i> 保存并生效
    </button>
  </div>`;
}

// 只重绘刚保存的那一张卡片。整块重绘会把另一张卡片尚未提交的输入按服务端旧值覆盖掉。
function replaceAIEngineCard(cfg) {
  const old = document.getElementById(`ai-engine-card-${cfg.id}`);
  if (!old) {
    loadAIEngineConfigs();
    return;
  }
  const holder = document.createElement("div");
  holder.innerHTML = aiEngineCard(cfg);
  old.replaceWith(holder.firstElementChild);
}

async function saveAIEngineConfig(id) {
  const payload = {
    display_name: inputVal(`ai-name-${id}`),
    provider: "openai_compatible",
    endpoint: inputVal(`ai-endpoint-${id}`),
    // 后端语义：api_key 传空 = 保留原密钥
    api_key: (document.getElementById(`ai-key-${id}`)?.value || "").trim(),
    model_name: inputVal(`ai-model-${id}`),
    system_prompt: document.getElementById(`ai-prompt-${id}`)?.value || "",
    temperature: parseFloat(document.getElementById(`ai-temp-${id}`)?.value) || 0.3,
    max_tokens: parseInt(document.getElementById(`ai-tokens-${id}`)?.value, 10) || 1024,
    is_enabled: document.getElementById(`ai-enabled-${id}`)?.checked || false,
  };

  if (payload.is_enabled && (!payload.endpoint || (!payload.api_key && !aiEngineConfigs.find(c => c.id === id)?.has_key))) {
    toast("启用引擎需要完整的 /chat/completions 地址与密钥；密钥留空表示沿用已存值", "warning");
    return;
  }

  const res = await request(`/tech/ai-configs/${id}`, { method: "PUT", body: JSON.stringify(payload) });
  const data = res ? await res.json().catch(() => ({})) : null;
  if (res && res.ok) {
    toast(data.message || "AI 引擎配置已更新并生效", "success");
    if (data.config) {
      const idx = aiEngineConfigs.findIndex(c => c.id === id);
      if (idx >= 0) aiEngineConfigs[idx] = data.config;
      replaceAIEngineCard(data.config);
    } else {
      loadAIEngineConfigs();
    }
  } else if (data) {
    toast(data.error || "保存失败", "error");
  }
}

function toggleAITestInputs() {
  const isVision = document.getElementById("ai-test-engine")?.value === "vision_engine";
  document.getElementById("ai-test-image")?.classList.toggle("hidden", !isVision);
  document.getElementById("ai-test-text")?.classList.toggle("hidden", isVision);
}

async function testAIEngine() {
  const key = document.getElementById("ai-test-engine")?.value || "vision_engine";
  const isVision = key === "vision_engine";
  const payload = { config_key: key };
  if (isVision) {
    payload.image_url = inputVal("ai-test-image");
    if (!payload.image_url) {
      toast("请填写一张公网可访问的测试图片 URL", "warning");
      return;
    }
  } else {
    payload.input_text = document.getElementById("ai-test-text")?.value || "";
    if (!payload.input_text.trim()) {
      toast("请粘贴一段巡查描述用于测试结构化", "warning");
      return;
    }
  }

  const statusEl = document.getElementById("ai-test-status");
  const resultEl = document.getElementById("ai-test-result");
  if (statusEl) statusEl.textContent = "真实外呼中，最长等待 30 秒...";
  if (resultEl) resultEl.classList.add("hidden");

  const res = await request("/tech/ai-playground/test", { method: "POST", body: JSON.stringify(payload) });
  const data = res ? await res.json().catch(() => ({})) : null;
  if (!res || !data) {
    if (statusEl) statusEl.textContent = "请求失败";
    return;
  }

  if (statusEl) {
    statusEl.innerHTML = data.status === "success"
      ? `<span class="text-emerald-600 font-bold">自检通过</span> · 已配置=${data.engine_configured} · 耗时 ${data.duration_ms}ms`
      : `<span class="text-rose-600 font-bold">自检失败</span> · 已配置=${data.engine_configured} · 耗时 ${data.duration_ms}ms`;
  }
  if (resultEl) {
    resultEl.classList.remove("hidden");
    resultEl.textContent = JSON.stringify(data, null, 2);
  }
}

// -----------------------------------------------------------------------------
// 宿管时段与提交规范后台配置 (CRUD)
// -----------------------------------------------------------------------------
let techSlotConfigsList = [];

async function loadTechSlotConfigs() {
  const res = await request("/tech/task-slots", { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();
  techSlotConfigsList = data.items || [];

  const badge = document.getElementById("tech-slots-total-badge");
  if (badge) badge.innerText = `${techSlotConfigsList.length} 条规则`;

  renderTechSlotsTable();
}

// 与后端 model.Period* 枚举一一对应；复数写法是历史别名，只用于把旧数据映射回规范值
const SLOT_PERIOD_LABELS = {
  daily: "每日通用",
  weekday: "仅周一至周五",
  weekend: "仅周六周日",
  single_week: "单周生效",
  double_week: "双周生效",
};
const SLOT_PERIOD_ALIASES = { weekdays: "weekday", weekends: "weekend" };

function normalizeSlotPeriod(value) {
  const key = SLOT_PERIOD_ALIASES[value] || value;
  return SLOT_PERIOD_LABELS[key] ? key : "daily";
}

function renderTechSlotsTable() {
  const wrap = document.getElementById("tech-slots-table-wrap");
  if (!wrap) return;

  if (techSlotConfigsList.length === 0) {
    wrap.innerHTML = `<div class="text-center py-12 text-zinc-400 text-xs">暂无时段规范配置，请点击右上角新增</div>`;
    return;
  }

  const urgencyBadgeMap = {
    normal: `<span class="pill-badge pill-badge-gray text-[10px]">常规</span>`,
    high: `<span class="pill-badge pill-badge-amber text-[10px]">重点关注</span>`,
    critical: `<span class="pill-badge pill-badge-dark text-[10px] bg-red-600 text-white">高危关键</span>`,
  };

  wrap.innerHTML = `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
        <tr>
          <th class="p-2.5">排序</th>
          <th class="p-2.5">时段名称</th>
          <th class="p-2.5">时间区间</th>
          <th class="p-2.5">适用周期</th>
          <th class="p-2.5">必须提交资料规范</th>
          <th class="p-2.5">关联类型</th>
          <th class="p-2.5">状态</th>
          <th class="p-2.5 text-right">操作</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100">
        ${techSlotConfigsList.map(s => `
          <tr class="hover:bg-zinc-50 transition">
            <td class="p-2.5 font-mono text-zinc-400">#${s.sort_order}</td>
            <td class="p-2.5 font-bold text-black flex items-center gap-1.5">
              ${urgencyBadgeMap[s.urgency_level] || ''}
              <span>${s.slot_name}</span>
            </td>
            <td class="p-2.5 font-mono font-bold text-black">${s.start_time} ~ ${s.end_time}</td>
            <td class="p-2.5"><span class="pill-badge pill-badge-gray text-[10px]">${SLOT_PERIOD_LABELS[normalizeSlotPeriod(s.period_type)]}</span></td>
            <td class="p-2.5 text-zinc-700 max-w-xs truncate" title="${s.required_materials}">
              ${s.required_materials}
            </td>
            <td class="p-2.5 font-mono text-zinc-500">${s.target_photo_type}</td>
            <td class="p-2.5">
              <span class="pill-badge ${s.is_enabled ? 'pill-badge-green' : 'pill-badge-gray'} text-[10px]">
                ${s.is_enabled ? '已启用' : '已停用'}
              </span>
            </td>
            <td class="p-2.5 text-right space-x-1.5">
              <button onclick="editSlotConfig(${s.id})" class="btn-pill btn-pill-light text-xs py-1 px-2.5 font-semibold">编辑</button>
              <button onclick="deleteSlotConfig(${s.id})" class="btn-pill btn-pill-light text-xs py-1 px-2.5 text-red-600 hover:border-red-500 font-semibold">删除</button>
            </td>
          </tr>
        `).join("")}
      </tbody>
    </table>
  `;
}

function openSlotConfigModal(slot = null) {
  const modal = document.getElementById("modal-slot-config-edit");
  if (!modal) return;
  modal.classList.remove("hidden");

  const titleEl = document.getElementById("modal-slot-title");
  const tagEl = document.getElementById("modal-slot-action-tag");
  const idEl = document.getElementById("slot-edit-id");

  if (slot) {
    if (titleEl) titleEl.innerText = "修改时段与提交规范";
    if (tagEl) tagEl.innerText = `ID #${slot.id}`;
    if (idEl) idEl.value = slot.id;

    document.getElementById("slot-inp-name").value = slot.slot_name || "";
    document.getElementById("slot-inp-start").value = slot.start_time || "";
    document.getElementById("slot-inp-end").value = slot.end_time || "";
    document.getElementById("slot-sel-period").value = normalizeSlotPeriod(slot.period_type);
    document.getElementById("slot-sel-urgency").value = slot.urgency_level || "normal";
    document.getElementById("slot-inp-materials").value = slot.required_materials || "";
    document.getElementById("slot-inp-prompt").value = slot.action_prompt || "";
    document.getElementById("slot-sel-target-type").value = slot.target_photo_type || "violation";
    document.getElementById("slot-inp-sort").value = slot.sort_order || 1;
    document.getElementById("slot-chk-enabled").checked = slot.is_enabled !== false;
  } else {
    if (titleEl) titleEl.innerText = "新增宿管时段与提交规范";
    if (tagEl) tagEl.innerText = "新建配置";
    if (idEl) idEl.value = "";

    document.getElementById("slot-inp-name").value = "";
    document.getElementById("slot-inp-start").value = "19:00";
    document.getElementById("slot-inp-end").value = "22:30";
    document.getElementById("slot-sel-period").value = "daily";
    document.getElementById("slot-sel-urgency").value = "normal";
    document.getElementById("slot-inp-materials").value = "";
    document.getElementById("slot-inp-prompt").value = "";
    document.getElementById("slot-sel-target-type").value = "violation";
    document.getElementById("slot-inp-sort").value = (techSlotConfigsList.length + 1);
    document.getElementById("slot-chk-enabled").checked = true;
  }
}

function closeSlotConfigModal() {
  const modal = document.getElementById("modal-slot-config-edit");
  if (modal) modal.classList.add("hidden");
}

function editSlotConfig(id) {
  const target = techSlotConfigsList.find(s => s.id === id);
  if (target) openSlotConfigModal(target);
}

async function handleSlotConfigSubmit(e) {
  e.preventDefault();
  const id = document.getElementById("slot-edit-id").value;
  const payload = {
    slot_name: document.getElementById("slot-inp-name").value.trim(),
    start_time: document.getElementById("slot-inp-start").value.trim(),
    end_time: document.getElementById("slot-inp-end").value.trim(),
    period_type: document.getElementById("slot-sel-period").value,
    urgency_level: document.getElementById("slot-sel-urgency").value,
    required_materials: document.getElementById("slot-inp-materials").value.trim(),
    action_prompt: document.getElementById("slot-inp-prompt").value.trim(),
    target_photo_type: document.getElementById("slot-sel-target-type").value,
    sort_order: parseInt(document.getElementById("slot-inp-sort").value) || 0,
    is_enabled: document.getElementById("slot-chk-enabled").checked,
  };

  let res;
  if (id) {
    res = await request(`/tech/task-slots/${id}`, {
      method: "PUT",
      body: JSON.stringify(payload),
    });
  } else {
    res = await request("/tech/task-slots", {
      method: "POST",
      body: JSON.stringify(payload),
    });
  }

  if (res && res.ok) {
    toast("时段规范配置已成功保存！宿管工作台将即时生效匹配。", "success");
    closeSlotConfigModal();
    loadTechSlotConfigs();
    if (state.currentTab === "dorm") {
      loadDormSlotNotice();
    }
  } else if (res) {
    const err = await res.json();
    toast("保存失败: " + (err.error || "未知异常"), "error");
  }
}

async function deleteSlotConfig(id) {
  if (!confirm(`确定要删除此时段规范配置 (ID #${id}) 吗？`)) return;
  const res = await request(`/tech/task-slots/${id}`, { method: "DELETE" });
  if (res && res.ok) {
    toast("已删除该配置！", "success");
    loadTechSlotConfigs();
  }
}

async function loadTechDBTables() {
  const res = await request("/tech/db/tables", { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();
  currentTechDBTables = data.items || [];

  const tag = document.getElementById("tech-db-total-tables-tag");
  if (tag) tag.innerText = `${currentTechDBTables.length} 个数据表`;

  renderTechTableList();

  // 默认选中当前表或第一张表
  if (!currentActiveDBTableMeta && currentTechDBTables.length > 0) {
    const defaultTable = currentTechDBTables.find(t => t.key === currentActiveDBTableKey) || currentTechDBTables[0];
    selectTechDBTable(defaultTable.key);
  } else if (currentActiveDBTableMeta) {
    const updated = currentTechDBTables.find(t => t.key === currentActiveDBTableKey);
    if (updated) currentActiveDBTableMeta = updated;
    loadCurrentDBTableRecords();
  }
}

function renderTechTableList(filter = "") {
  const navWrap = document.getElementById("tech-db-table-nav-list");
  if (!navWrap) return;

  const filtered = currentTechDBTables.filter(t => {
    if (!filter) return true;
    return t.title.includes(filter) || t.key.includes(filter) || (t.description && t.description.includes(filter));
  });

  if (filtered.length === 0) {
    navWrap.innerHTML = `<div class="p-3 text-center text-zinc-400 text-xs">未找到匹配的名单库</div>`;
    return;
  }

  navWrap.innerHTML = filtered.map(t => {
    const isActive = t.key === currentActiveDBTableKey;
    return `
      <div onclick="selectTechDBTable('${t.key}')" class="db-table-item ${isActive ? 'active' : ''}">
        <div class="flex items-center space-x-2.5 truncate">
          <i class="fa-solid ${t.icon || 'fa-table'} text-sm ${isActive ? 'text-white' : 'text-zinc-600'}"></i>
          <span class="truncate font-medium">${t.title}</span>
        </div>
        <span class="db-table-count">${t.count} 条</span>
      </div>
    `;
  }).join("");
}

function filterTechTableList() {
  const inp = document.getElementById("tech-db-table-filter-input");
  renderTechTableList(inp ? inp.value.trim() : "");
}

function selectTechDBTable(tableKey) {
  currentActiveDBTableKey = tableKey;
  currentActiveDBTableMeta = currentTechDBTables.find(t => t.key === tableKey);
  if (!currentActiveDBTableMeta) return;

  // 更新左侧分栏高亮
  renderTechTableList(document.getElementById("tech-db-table-filter-input")?.value.trim() || "");

  // 更新右侧头部
  const iconEl = document.getElementById("tech-db-active-icon");
  if (iconEl) iconEl.innerHTML = `<i class="fa-solid ${currentActiveDBTableMeta.icon || 'fa-table'}"></i>`;
  const titleEl = document.getElementById("tech-db-active-title");
  if (titleEl) titleEl.innerText = currentActiveDBTableMeta.title;
  const keyEl = document.getElementById("tech-db-active-key");
  if (keyEl) keyEl.innerText = currentActiveDBTableMeta.key;
  const countEl = document.getElementById("tech-db-active-count");
  if (countEl) countEl.innerText = `${currentActiveDBTableMeta.count} 条记录`;
  const descEl = document.getElementById("tech-db-active-desc");
  if (descEl) descEl.innerText = currentActiveDBTableMeta.description;

  // 重置分页与搜索
  currentDBPage = 1;
  currentDBSearchQuery = "";
  const searchInp = document.getElementById("tech-db-record-search-input");
  if (searchInp) searchInp.value = "";

  loadCurrentDBTableRecords();
}

function debounceTechDBSearch() {
  clearTimeout(dbSearchDebounceTimer);
  dbSearchDebounceTimer = setTimeout(() => {
    const inp = document.getElementById("tech-db-record-search-input");
    currentDBSearchQuery = inp ? inp.value.trim() : "";
    currentDBPage = 1;
    loadCurrentDBTableRecords();
  }, 250);
}

function prevTechDBPage() {
  if (currentDBPage > 1) {
    currentDBPage--;
    loadCurrentDBTableRecords();
  }
}

function nextTechDBPage() {
  if (currentDBPage < currentDBTotalPages) {
    currentDBPage++;
    loadCurrentDBTableRecords();
  }
}

function refreshCurrentDBTable() {
  loadTechDBTables();
}

async function loadCurrentDBTableRecords() {
  if (!currentActiveDBTableKey) return;
  const tableWrap = document.getElementById("tech-db-records-table-wrap");
  if (!tableWrap) return;

  const url = `/tech/db/tables/${currentActiveDBTableKey}?page=${currentDBPage}&page_size=${currentDBPageSize}&q=${encodeURIComponent(currentDBSearchQuery)}`;
  const res = await request(url, { method: "GET" });
  if (!res || !res.ok) {
    tableWrap.innerHTML = `<div class="p-8 text-center text-red-500 text-xs">加载数据失败，请检查网络或后端状态</div>`;
    return;
  }

  const data = await res.json();
  currentDBTableRecords = data.items || [];
  const total = data.total || 0;
  currentDBTotalPages = Math.ceil(total / currentDBPageSize) || 1;

  // 更新分页展示
  const pageNumEl = document.getElementById("tech-db-page-num");
  if (pageNumEl) pageNumEl.innerText = currentDBPage;
  const summaryEl = document.getElementById("tech-db-pagination-summary");
  if (summaryEl) summaryEl.innerText = `第 ${currentDBPage} / ${currentDBTotalPages} 页 (每页 ${currentDBPageSize} 条)`;
  const footerCountEl = document.getElementById("tech-db-records-footer-count");
  if (footerCountEl) footerCountEl.innerText = `当前检索结果共 ${total} 条数据记录`;
  const countEl = document.getElementById("tech-db-active-count");
  if (countEl && !currentDBSearchQuery) countEl.innerText = `${total} 条记录`;

  const btnPrev = document.getElementById("btn-tech-db-prev");
  const btnNext = document.getElementById("btn-tech-db-next");
  if (btnPrev) btnPrev.disabled = currentDBPage <= 1;
  if (btnNext) btnNext.disabled = currentDBPage >= currentDBTotalPages;

  if (currentDBTableRecords.length === 0) {
    tableWrap.innerHTML = `
      <div class="py-12 text-center text-zinc-400 space-y-2">
        <i class="fa-solid fa-folder-open text-2xl text-zinc-300"></i>
        <p class="text-xs">当前名单库中暂无数据记录${currentDBSearchQuery ? ' (无匹配关键词)' : ''}</p>
        <button onclick="openCreateDBRecordModal()" class="btn-pill btn-pill-dark text-xs py-1.5 px-3 mt-2">
          <i class="fa-solid fa-plus mr-1"></i> 立即新增首条记录
        </button>
      </div>
    `;
    return;
  }

  // 根据当前表 Schema 动态渲染表头与内容
  const cols = currentActiveDBTableMeta ? currentActiveDBTableMeta.columns || [] : [];
  
  tableWrap.innerHTML = `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase sticky top-0">
        <tr>
          <th class="p-2.5 font-mono text-zinc-400 w-12">ID</th>
          ${cols.map(c => `<th class="p-2.5 whitespace-nowrap">${c.label}</th>`).join("")}
          <th class="p-2.5 whitespace-nowrap">创建时间</th>
          <th class="p-2.5 text-right whitespace-nowrap w-28">数据运维操作</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100">
        ${currentDBTableRecords.map(r => `
          <tr class="hover:bg-zinc-50/80 transition">
            <td class="p-2.5 font-mono text-zinc-400 text-[11px]">#${r.id}</td>
            ${cols.map(c => {
              const val = r[c.key];
              return `<td class="p-2.5 text-zinc-700">${formatDBTableCellValue(c, val)}</td>`;
            }).join("")}
            <td class="p-2.5 font-mono text-zinc-400 text-[10px] whitespace-nowrap">${r.created_at ? r.created_at.slice(0, 16).replace('T', ' ') : '-'}</td>
            <td class="p-2.5 text-right whitespace-nowrap space-x-1">
              <button onclick="openEditDBRecordModal(${r.id})" class="btn-pill btn-pill-light text-xs py-1 px-2.5">
                <i class="fa-solid fa-pen mr-1 text-[10px]"></i> 编辑
              </button>
              <button onclick="deleteDBRecord(${r.id})" class="btn-pill btn-pill-light text-xs py-1 px-2 text-rose-500 hover:text-rose-600 hover:bg-rose-50" title="删除记录">
                <i class="fa-regular fa-trash-can"></i>
              </button>
            </td>
          </tr>
        `).join("")}
      </tbody>
    </table>
  `;
}

function formatDBTableCellValue(col, val) {
  if (val === undefined || val === null || val === "") return `<span class="text-zinc-300">-</span>`;

  if (col.type === "boolean") {
    return val
      ? `<span class="pill-badge pill-badge-green text-[10px]">是 / 已激活</span>`
      : `<span class="pill-badge pill-badge-gray text-[10px]">否 / 未激活</span>`;
  }

  if (col.key === "status") {
    if (val === "active" || val === "completed" || val === "approved" || val === "admitted") {
      return `<span class="pill-badge pill-badge-green text-[10px]">${val}</span>`;
    } else if (val === "pending" || val === "in_progress" || val === "submitted") {
      return `<span class="pill-badge pill-badge-amber text-[10px]">${val}</span>`;
    } else if (val === "disabled" || val === "rejected" || val === "missed") {
      return `<span class="pill-badge pill-badge-gray text-[10px] text-red-500">${val}</span>`;
    }
  }

  if (col.key === "role") {
    return `<span class="pill-badge pill-badge-dark text-[10px]">${val}</span>`;
  }

  if (col.key === "severity") {
    if (val === "high" || val === "critical") {
      return `<span class="pill-badge pill-badge-dark text-[10px] font-bold text-amber-400">${val}</span>`;
    }
    return `<span class="pill-badge pill-badge-gray text-[10px]">${val}</span>`;
  }

  if (col.key === "total_score" || col.key === "score_change" || col.key === "balance_after") {
    return `<span class="font-mono font-bold text-black">${val}</span>`;
  }

  if (col.key === "phone" || col.key === "student_no") {
    return `<span class="font-mono text-zinc-600">${val}</span>`;
  }

  const str = String(val);
  if (str.length > 30) {
    return `<span title="${str.replace(/"/g, '&quot;')}">${str.slice(0, 28)}...</span>`;
  }
  return str;
}

// 模态弹窗管理：新增与修改
function openCreateDBRecordModal() {
  if (!currentActiveDBTableMeta) return;
  currentEditingRecordId = null;

  document.getElementById("modal-tech-db-action-tag").innerText = "新建录入";
  document.getElementById("modal-tech-db-title").innerText = `向【${currentActiveDBTableMeta.title}】录入新记录`;

  renderDBFormFields({});
  document.getElementById("modal-tech-db-edit").classList.remove("hidden");
}

function openEditDBRecordModal(id) {
  if (!currentActiveDBTableMeta) return;
  currentEditingRecordId = id;

  const record = currentDBTableRecords.find(r => r.id === id);
  if (!record) return;

  document.getElementById("modal-tech-db-action-tag").innerText = `编辑修改 (ID: ${id})`;
  document.getElementById("modal-tech-db-title").innerText = `修改【${currentActiveDBTableMeta.title}】记录`;

  renderDBFormFields(record);
  document.getElementById("modal-tech-db-edit").classList.remove("hidden");
}

function closeTechDBEditModal() {
  document.getElementById("modal-tech-db-edit").classList.add("hidden");
  currentEditingRecordId = null;
}

function renderDBFormFields(data = {}) {
  const container = document.getElementById("tech-db-dynamic-form-fields");
  if (!container) return;

  const cols = currentActiveDBTableMeta ? currentActiveDBTableMeta.columns || [] : [];
  
  container.innerHTML = cols.map(c => {
    const val = data[c.key] !== undefined && data[c.key] !== null ? data[c.key] : "";
    const isFullWidth = c.key === "reason" || c.key === "review_comment" || c.key === "shift_info" || c.key === "member_names";

    // 权限属性列只展示不编辑：不渲染控件，提交体里自然不会出现该字段
    if (c.read_only) {
      const shown = val === "" || val === undefined ? "（新建时由服务端指定）" : String(val);
      return `
        <div class="${isFullWidth ? 'sm:col-span-2' : ''}">
          <label class="block font-bold text-zinc-700 mb-1">${c.label}</label>
          <div class="w-full px-3 py-2.5 rounded-xl border border-dashed border-zinc-300 bg-zinc-50 text-xs text-zinc-500 font-medium">${escapeHtml(shown)}</div>
          <p class="text-[10px] text-zinc-400 mt-1">权限属性不在此处变更，请用带口令确认并留痕的专门入口</p>
        </div>
      `;
    }

    if (c.type === "select" && c.options && c.options.length > 0) {
      return `
        <div class="${isFullWidth ? 'sm:col-span-2' : ''}">
          <label class="block font-bold text-zinc-700 mb-1">${c.label} ${c.required ? '<span class="text-red-500">*</span>' : ''}</label>
          <select name="${c.key}" ${c.required ? 'required' : ''} class="w-full px-3 py-2.5 rounded-xl border border-zinc-200 bg-white font-medium text-xs">
            <option value="">-- 请选择${c.label} --</option>
            ${c.options.map(opt => `<option value="${opt}" ${String(val) === String(opt) ? 'selected' : ''}>${opt}</option>`).join("")}
          </select>
        </div>
      `;
    }

    if (c.type === "boolean") {
      return `
        <div class="${isFullWidth ? 'sm:col-span-2' : ''}">
          <label class="block font-bold text-zinc-700 mb-1">${c.label}</label>
          <select name="${c.key}" class="w-full px-3 py-2.5 rounded-xl border border-zinc-200 bg-white font-medium text-xs">
            <option value="true" ${val === true || val === "true" ? 'selected' : ''}>是 (True)</option>
            <option value="false" ${val === false || val === "false" ? 'selected' : ''}>否 (False)</option>
          </select>
        </div>
      `;
    }

    if (c.type === "number") {
      return `
        <div class="${isFullWidth ? 'sm:col-span-2' : ''}">
          <label class="block font-bold text-zinc-700 mb-1">${c.label} ${c.required ? '<span class="text-red-500">*</span>' : ''}</label>
          <input type="number" name="${c.key}" value="${val}" ${c.required ? 'required' : ''} placeholder="请输入数字分值..." class="w-full px-3 py-2.5 rounded-xl border border-zinc-200 bg-white font-mono text-xs">
        </div>
      `;
    }

    return `
      <div class="${isFullWidth ? 'sm:col-span-2' : ''}">
        <label class="block font-bold text-zinc-700 mb-1">${c.label} ${c.required ? '<span class="text-red-500">*</span>' : ''}</label>
        <input type="text" name="${c.key}" value="${val}" ${c.required ? 'required' : ''} placeholder="请输入${c.label}..." class="w-full px-3 py-2.5 rounded-xl border border-zinc-200 bg-white text-xs">
      </div>
    `;
  }).join("");
}

async function handleTechDBRecordSubmit(e) {
  e.preventDefault();
  if (!currentActiveDBTableKey) return;

  const form = document.getElementById("form-tech-db-record");
  const formData = new FormData(form);
  const payload = {};

  const cols = currentActiveDBTableMeta ? currentActiveDBTableMeta.columns || [] : [];
  cols.forEach(c => {
    let val = formData.get(c.key);
    if (val === null || val === undefined) return;
    val = String(val).trim();

    if (c.type === "number") {
      payload[c.key] = val !== "" ? parseInt(val, 10) : 0;
    } else if (c.type === "boolean") {
      payload[c.key] = val === "true";
    } else {
      payload[c.key] = val;
    }
  });

  let url = `/tech/db/tables/${currentActiveDBTableKey}`;
  let method = "POST";

  if (currentEditingRecordId) {
    url = `/tech/db/tables/${currentActiveDBTableKey}/${currentEditingRecordId}`;
    method = "PUT";
  }

  const res = await request(url, {
    method,
    body: JSON.stringify(payload),
  });

  if (res && res.ok) {
    toast(currentEditingRecordId ? "数据记录已成功更新！" : "数据记录已成功入库！", "success");
    closeTechDBEditModal();
    loadTechDBTables();
  }
}

async function deleteDBRecord(id) {
  if (!currentActiveDBTableKey) return;
  if (!confirm(`【安全警告】确定要从底层数据库中彻底删除此条记录 (ID: ${id}) 吗？该操作不可逆！`)) {
    return;
  }

  const res = await request(`/tech/db/tables/${currentActiveDBTableKey}/${id}`, {
    method: "DELETE",
  });

  if (res && res.ok) {
    toast("数据记录已成功删除！", "success");
    loadTechDBTables();
  }
}

// =============================================================================
// 7. 信息查看与导出中心 (角色: viewer_export)
// =============================================================================
async function loadExportTable() {
  const bldg = document.getElementById("exp-filter-bldg").value.trim();
  const sev = document.getElementById("exp-filter-sev").value;
  const cat = document.getElementById("exp-filter-cat").value.trim();

  const params = new URLSearchParams();
  if (bldg) params.append("building", bldg);
  if (sev) params.append("severity", sev);
  if (cat) params.append("category", cat);

  const res = await request(`/export/inspections?${params.toString()}`, { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();

  document.getElementById("exp-count-label").innerText = `已加载 ${data.total} 条留痕记录`;
  const wrap = document.getElementById("exp-table-wrap");

  wrap.innerHTML = `
    <table class="w-full text-xs text-left">
      <thead class="bg-zinc-50 text-zinc-500 font-semibold uppercase">
        <tr>
          <th class="p-2.5">记录编号</th>
          <th class="p-2.5">楼栋宿舍</th>
          <th class="p-2.5">隐患类别</th>
          <th class="p-2.5">严重度</th>
          <th class="p-2.5">扣分</th>
          <th class="p-2.5">上传宿管</th>
          <th class="p-2.5">AI 多模态分析与归纳摘要</th>
          <th class="p-2.5">时间</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-zinc-100">
        ${data.items.map(item => `
          <tr>
            <td class="p-2.5 font-mono text-zinc-400">#${item.id}</td>
            <td class="p-2.5 font-bold text-black">${item.building} ${item.room_number || ''}</td>
            <td class="p-2.5"><span class="pill-badge pill-badge-dark text-[10px]">${item.category}</span></td>
            <td class="p-2.5"><span class="pill-badge ${item.severity === 'high' ? 'pill-badge-dark' : 'pill-badge-gray'} text-[10px]">${item.severity}</span></td>
            <td class="p-2.5 font-mono font-bold text-black">-${item.deduct_points}</td>
            <td class="p-2.5">${item.manager_name}</td>
            <td class="p-2.5 text-zinc-600 max-w-xs truncate" title="${item.vision_ai_output}">${item.vision_ai_output}</td>
            <td class="p-2.5 font-mono text-zinc-400 text-[10px]">${item.created_at.slice(0, 16)}</td>
          </tr>
        `).join("")}
      </tbody>
    </table>
  `;
}

function downloadSecureCSV() {
  downloadAuthedFile(
    `${API_BASE}/export/download-csv`,
    `学管会园区安全与扣分归档_${new Date().toISOString().slice(0, 10)}.csv`,
    "导出失败"
  );
}

// 一键打包综合档案包 (ZIP)
async function downloadBundleZip() {
  const ok = await downloadAuthedFile(
    `${API_BASE}/export/bundle-zip`,
    `学管会综合管理档案打包_${new Date().toISOString().slice(0, 10)}.zip`,
    "打包下载失败"
  );
  if (ok) toast("✅ 综合档案包 (下周排班 + 本周纪检 + 全员上工) 已打包完成并触发下载！", "success");
}

// 导出每天上下午值班部员名单 (CSV)
function downloadDailyDutyCSV() {
  downloadAuthedFile(
    `${API_BASE}/export/daily-duty-csv`,
    `每天上下午值班部员名单_${new Date().toISOString().slice(0, 10)}.csv`,
    "导出失败"
  );
}

// 导出常驻值班骨干干事名册 (CSV)
function downloadStandingDutyCSV() {
  downloadAuthedFile(
    `${API_BASE}/export/standing-duty-csv`,
    `学管会常驻值班骨干干事名册_${new Date().toISOString().slice(0, 10)}.csv`,
    "导出失败"
  );
}

// 弹窗辅助
function showApkModal() {
  document.getElementById("modal-apk-info").classList.remove("hidden");
}
function closeApkModal() {
  document.getElementById("modal-apk-info").classList.add("hidden");
}

// =============================================================================
// 8. 高一~高三学生名册多格式智能特征识别导入与全景透视
// =============================================================================

let studentModuleState = {
  importMode: 'paste', // 'paste' 或 'file'
  selectedFile: null,
  cachedParsed: [],
  currentGradeFilter: '',
};

// 切换学生名册专区与数据加载
async function loadStudentSection() {
  loadStudentStatsAndTable();
}

// 切换导入模式 (文本粘贴 vs 文件拖拽)
function setStudentImportMode(mode) {
  studentModuleState.importMode = mode;
  const btnPaste = document.getElementById("btn-stu-paste");
  const btnFile = document.getElementById("btn-stu-file");
  const boxPaste = document.getElementById("stu-import-paste-box");
  const boxFile = document.getElementById("stu-import-file-box");

  if (mode === 'paste') {
    btnPaste.className = "nav-pill-item active";
    btnFile.className = "nav-pill-item";
    boxPaste.classList.remove("hidden");
    boxFile.classList.add("hidden");
  } else {
    btnFile.className = "nav-pill-item active";
    btnPaste.className = "nav-pill-item";
    boxFile.classList.remove("hidden");
    boxPaste.classList.add("hidden");
  }
}

// 自动填入高一~高三示例测试数据 (涵盖不同格式，验证特征识别能力)
function fillStudentSampleText() {
  const sample = `1号楼 301 李华 高一(1)班 男 1号床
1号楼 301 张明 高一(1)班 男 2号床
1号楼 302 王芳 高一(2)班 女 1号床
1号楼 302 赵小雨 高一(2)班 女 2号床
2号楼 405 陈天豪 高二(3)班 男 1号床 20240301
2号楼 405 周正 高二(3)班 男 2号床 20240302
2号楼 406 林浩然 高二(4)班 男 1号床 20240401
西区12栋 501 孙晓峰 高三(1)班 男 1号床 20230101
西区12栋 501 黄俊杰 高三(1)班 男 2号床 20230102
西区12栋 502 刘若涵 高三(5)班 女 1号床 20230501
楼栋\t寝室\t姓名\t班级
1号楼\t204\t钱宇航\t高一(5)班
2号楼\t306\t吴桐\t高二(8)班
西区12栋\t608\t郑文博\t高三(9)班`;

  const input = document.getElementById("stu-raw-paste-input");
  if (input) {
    input.value = sample;
    setStudentImportMode('paste');
  }
}

// 文件选择事件
function handleStudentFileSelect(event) {
  const file = event.target.files[0];
  if (file) {
    studentModuleState.selectedFile = file;
    document.getElementById("stu-file-name").innerText = `已选择: ${file.name} (${(file.size / 1024).toFixed(1)} KB)`;
    document.getElementById("stu-file-selected").classList.remove("hidden");
    document.getElementById("stu-file-prompt").classList.add("hidden");
  }
}

// 执行智能特征识别与预览
async function executeSmartParsePreview() {
  const btn = document.getElementById("btn-parse-students");
  btn.disabled = true;
  btn.innerHTML = `<i class="fa-solid fa-spinner animate-spin mr-1.5"></i> 算法自动识别提取特征中...`;

  let res;
  if (studentModuleState.importMode === 'file' && studentModuleState.selectedFile) {
    const formData = new FormData();
    formData.append("file", studentModuleState.selectedFile);
    res = await request("/students/parse-preview", {
      method: "POST",
      body: formData,
    });
  } else {
    const rawText = document.getElementById("stu-raw-paste-input").value.trim();
    if (!rawText) {
      toast("请先粘贴文本或上传待识别名单文件！", "warning");
      btn.disabled = false;
      btn.innerHTML = `<i class="fa-solid fa-bolt mr-1.5"></i> 执行智能特征识别与解析预览`;
      return;
    }
    res = await request("/students/parse-preview", {
      method: "POST",
      body: JSON.stringify({ raw_text: rawText }),
    });
  }

  btn.disabled = false;
  btn.innerHTML = `<i class="fa-solid fa-bolt mr-1.5"></i> 执行智能特征识别与解析预览`;

  if (res && res.ok) {
    const data = await res.json();
    studentModuleState.cachedParsed = data.all_parsed;
    renderParsePreviewReport(data);
  }
}

// 渲染特征识别提取结果报表与样本映射表
function renderParsePreviewReport(data) {
  const panel = document.getElementById("stu-preview-report-panel");
  panel.classList.remove("hidden");

  document.getElementById("stu-recognized-badge").innerText = `成功识别 ${data.total_recognized} 名学生`;

  // 年级分布徽章
  const statsEl = document.getElementById("stu-recognized-grade-tags");
  const s = data.grade_stats;
  statsEl.innerHTML = `
    <span class="pill-badge pill-badge-dark text-xs">高一: ${s["高一"] || 0}人</span>
    <span class="pill-badge pill-badge-dark text-xs">高二: ${s["高二"] || 0}人</span>
    <span class="pill-badge pill-badge-dark text-xs">高三: ${s["高三"] || 0}人</span>
    ${s["其他"] ? `<span class="pill-badge pill-badge-gray text-xs">其他: ${s["其他"]}人</span>` : ''}
  `;

  // 样本表格渲染
  const tbody = document.getElementById("stu-preview-table-body");
  tbody.innerHTML = data.preview_sample.map((st, idx) => `
    <tr class="hover:bg-zinc-100 transition">
      <td class="p-2 font-mono text-zinc-400">#${idx + 1}</td>
      <td class="p-2"><span class="pill-badge pill-badge-dark text-[10px]">${st.grade}</span></td>
      <td class="p-2 font-bold text-black">${st.class_name}</td>
      <td class="p-2 font-semibold">${st.building}</td>
      <td class="p-2 font-mono font-bold text-black">${st.room_number}室</td>
      <td class="p-2 font-bold text-black">${st.real_name}</td>
      <td class="p-2 text-zinc-400 font-mono text-[10px] max-w-xs truncate" title="${st.source_line}">${st.source_line}</td>
    </tr>
  `).join("");

  panel.scrollIntoView({ behavior: "smooth" });
}

// 确认批量入库
async function confirmBatchImport(overwrite) {
  if (studentModuleState.cachedParsed.length === 0) {
    toast("暂无可入库的学生数据，请先执行特征识别！", "warning");
    return;
  }

  if (overwrite && !confirm("警告：覆盖模式将清空现有学生名册并重新写入，确定继续吗？")) {
    return;
  }

  const payload = {
    students: studentModuleState.cachedParsed,
    overwrite: overwrite,
  };

  const res = await request("/students/batch-import", {
    method: "POST",
    body: JSON.stringify(payload),
  });

  if (res && res.ok) {
    const data = await res.json();
    toast(data.message || "批量入库完成！", "success");
    document.getElementById("stu-preview-report-panel").classList.add("hidden");
    document.getElementById("stu-raw-paste-input").value = "";
    loadStudentStatsAndTable();
  }
}

// 加载名册统计与大表数据
async function loadStudentStatsAndTable() {
  loadStudentTableData();
}

async function loadStudentTableData() {
  const bldg = document.getElementById("filter-stu-bldg") ? document.getElementById("filter-stu-bldg").value.trim() : "";
  const room = document.getElementById("filter-stu-room") ? document.getElementById("filter-stu-room").value.trim() : "";
  const className = document.getElementById("filter-stu-class") ? document.getElementById("filter-stu-class").value.trim() : "";
  const kw = document.getElementById("filter-stu-kw") ? document.getElementById("filter-stu-kw").value.trim() : "";

  const params = new URLSearchParams();
  if (studentModuleState.currentGradeFilter) params.append("grade", studentModuleState.currentGradeFilter);
  if (bldg) params.append("building", bldg);
  if (room) params.append("room_number", room);
  if (className) params.append("class_name", className);
  if (kw) params.append("keyword", kw);

  const res = await request(`/students?${params.toString()}`, { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();

  // 更新四大年级人数统计
  document.getElementById("stu-total-count").innerText = data.total;
  let g1 = 0, g2 = 0, g3 = 0;
  if (data.grade_stats) {
    data.grade_stats.forEach(g => {
      if (g.grade === "高一") g1 = g.count;
      if (g.grade === "高二") g2 = g.count;
      if (g.grade === "高三") g3 = g.count;
    });
  }
  document.getElementById("stu-grade1-count").innerText = g1;
  document.getElementById("stu-grade2-count").innerText = g2;
  document.getElementById("stu-grade3-count").innerText = g3;
  document.getElementById("stu-filter-total-label").innerText = `共检索到 ${data.total} 名学生`;

  // 渲染大表
  const tbody = document.getElementById("stu-master-table-body");
  if (data.items.length === 0) {
    tbody.innerHTML = `<tr><td colspan="8" class="p-6 text-center text-zinc-400 text-xs">暂无符合条件的学生信息，请使用上方智能识别器导入名单</td></tr>`;
    return;
  }

  tbody.innerHTML = data.items.map(st => `
    <tr class="hover:bg-zinc-50 transition">
      <td class="p-2.5 font-mono text-zinc-400">#${st.id}</td>
      <td class="p-2.5"><span class="pill-badge pill-badge-dark text-[10px]">${st.grade}</span></td>
      <td class="p-2.5 font-bold text-black">${st.class_name}</td>
      <td class="p-2.5">${st.building}</td>
      <td class="p-2.5 font-mono font-bold text-black">${st.room_number}室</td>
      <td class="p-2.5 font-bold text-black">${st.real_name}</td>
      <td class="p-2.5"><span class="pill-badge pill-badge-green text-[10px]">正常在籍</span></td>
      <td class="p-2.5 text-right">
        <button onclick="lookupRoomMates('${st.building}', '${st.room_number}')" class="btn-pill btn-pill-light text-[11px] py-1 px-3">
          <i class="fa-solid fa-users-rays mr-1"></i> 查舍友
        </button>
      </td>
    </tr>
  `).join("");
}

// 年级过滤胶囊切换
function filterStudentsByGrade(grade) {
  studentModuleState.currentGradeFilter = grade;
  document.getElementById("tab-grade-all").className = !grade ? "nav-pill-item active" : "nav-pill-item";
  document.getElementById("tab-grade-1").className = grade === "高一" ? "nav-pill-item active" : "nav-pill-item";
  document.getElementById("tab-grade-2").className = grade === "高二" ? "nav-pill-item active" : "nav-pill-item";
  document.getElementById("tab-grade-3").className = grade === "高三" ? "nav-pill-item active" : "nav-pill-item";
  loadStudentTableData();
}

// 点击一键查同寝舍友
async function lookupRoomMates(building, room) {
  const res = await request(`/students/room-members?building=${encodeURIComponent(building)}&room_number=${encodeURIComponent(room)}`, { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();
  const names = data.students.map(s => `${s.real_name} (${s.class_name})`).join("、\n");
  toast(`【${building} ${room}室 同寝舍友名单 (共${data.count}人)】\n${names || '暂无其他人员'}`, "info", 8000);
}

// 宿管工作台查寝联动：输入房间号时自动提示该寝室学生
let lookupDebounceTimer = null;
async function lookupRoomStudentsLive() {
  clearTimeout(lookupDebounceTimer);
  lookupDebounceTimer = setTimeout(async () => {
    const roomInput = document.getElementById("dorm-inp-room");
    if (!roomInput) return;
    const room = roomInput.value.trim();
    const hintEl = document.getElementById("dorm-room-students-hint");
    if (!hintEl) return;

    if (!room || room.length < 2) {
      hintEl.classList.add("hidden");
      return;
    }

    const bldg = state.user ? state.user.building : "";
    const res = await request(`/students/room-members?building=${encodeURIComponent(bldg)}&room_number=${encodeURIComponent(room)}`, { method: "GET" });
    if (res && res.ok) {
      const data = await res.json();
      if (data.count > 0) {
        hintEl.classList.remove("hidden");
        const listStr = data.students.map(s => `<span class="pill-badge pill-badge-dark mr-1">${s.real_name} <span class="opacity-70 text-[9px]">(${s.class_name})</span></span>`).join("");
        hintEl.innerHTML = `
          <div class="text-zinc-500 text-[10px] font-bold uppercase"><i class="fa-solid fa-users-viewfinder mr-1"></i>【${room}室 名册联动匹配到 ${data.count} 名在册学生】</div>
          <div class="flex flex-wrap gap-1 mt-1">${listStr}</div>
        `;
      } else {
        hintEl.classList.add("hidden");
      }
    }
  }, 250);
}

// 清空名册确认
async function clearStudentsConfirm() {
  if (!confirm("确定要清空全部学生名册吗？该操作不可逆，已产生的扣分记录会保留但退回仅按姓名存底。")) return;

  let res = await request("/students/clear", {
    method: "DELETE",
    body: JSON.stringify({ confirm: "DELETE_ALL_ROSTER" }),
  });
  let data = res ? await res.json().catch(() => ({})) : null;

  // 409 = 存在已关联名册的打表记录，需要再显式确认一次解除关联
  if (res && res.status === 409) {
    if (!confirm((data.error || "有打表记录已关联名册") + "\n\n确定仍要清空吗？")) return;
    res = await request("/students/clear", {
      method: "DELETE",
      body: JSON.stringify({ confirm: "DELETE_ALL_ROSTER", unlink_linked: true }),
    });
    data = res ? await res.json().catch(() => ({})) : null;
  }

  if (res && res.ok) {
    toast(data.message || "学生名册已清空", "success");
    loadStudentStatsAndTable();
  } else if (data) {
    toast(data.error || "清空名册失败", "error");
  }
}

// =============================================================================
// 9. 技术部 AI 福利中枢与积分购买额度商城 (谁设置、谁用、谁管理)
// =============================================================================
let currentWelfareGateways = [];
let currentActiveWelfareGateway = null;
let currentRelayChatHistory = [];

async function loadWelfarePanel() {
  if (!state.user) return;

  // 1. 刷新部员当前积分
  const scoreBadge = document.getElementById("welfare-user-score-badge");
  if (scoreBadge) {
    scoreBadge.innerText = state.user.total_score || 0;
  }

  // 2. 身份隔离：技术组展示上传按钮与所有者管理说明
  const isTechAdmin = (state.user.role === "tech_admin");
  const btnAddGw = document.getElementById("btn-welfare-add-gateway");
  const btnPricingSettings = document.getElementById("btn-welfare-pricing-settings");
  const idTitle = document.getElementById("welfare-identity-title");
  const idDesc = document.getElementById("welfare-identity-desc");
  const roleTag = document.getElementById("welfare-role-tag");

  if (btnAddGw) {
    if (isTechAdmin) {
      btnAddGw.classList.remove("hidden");
    } else {
      btnAddGw.classList.add("hidden");
    }
  }

  if (btnPricingSettings) {
    if (isTechAdmin) {
      btnPricingSettings.classList.remove("hidden");
    } else {
      btnPricingSettings.classList.add("hidden");
    }
  }

  if (roleTag) {
    roleTag.innerText = isTechAdmin ? "技术部管理员权限" : "学管会部员专享";
  }
  if (idTitle) {
    idTitle.innerText = isTechAdmin ? "技术部长专属中转配置" : "查寝履职积分换 AI 算力";
  }
  if (idDesc) {
    idDesc.innerText = isTechAdmin 
      ? "您作为技术维护组负责人，可上传自己的 Endpoint 与 Key，自定义各模型兑换价格与调用次数，服务端加密透传给部员作为专属福利。" 
      : "部员使用日常查寝、文明督查积攒的考核积分，随时自主兑换昂贵的商业 AI 调用额度辅导学业与代码！";
  }

  // 3. 加载各模型剩余调用次数工作台与网关
  await loadModelPricingsAndQuotas();
  await loadWelfareGateways();
}

// -----------------------------------------------------------------------------
// 各模型剩余调用次数工作台 (部员看板 & 技术部自定义价格兑换)
// -----------------------------------------------------------------------------
let currentModelPricingsList = [];

async function loadModelPricingsAndQuotas() {
  const res = await request("/welfare/pricings", { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();
  currentModelPricingsList = data.items || [];

  const countBadge = document.getElementById("welfare-model-count-badge");
  if (countBadge) countBadge.innerText = `${currentModelPricingsList.length} 个模型已上架`;

  renderModelQuotaCards(currentModelPricingsList);
}

function renderModelQuotaCards(pricings) {
  const container = document.getElementById("welfare-model-quota-cards-wrap");
  if (!container) return;

  if (!pricings || pricings.length === 0) {
    const userDept = (state.user && state.user.department) ? state.user.department : "本部";
    container.innerHTML = `
      <div class="text-center py-12 text-zinc-400 text-xs col-span-full space-y-2">
        <i class="fa-solid fa-gift text-2xl text-zinc-300"></i>
        <p class="font-bold text-black">${userDept}部长暂未配置可用 AI 模型定价与福利</p>
        <p class="text-[11px] text-zinc-400">本专区模型由各部门部长自主上架并设定兑换价格。请联系【${userDept}部长】设置后兑换使用。</p>
      </div>
    `;
    return;
  }

  const iconMap = {
    bolt: "fa-bolt text-amber-500",
    microchip: "fa-microchip text-sky-500",
    crown: "fa-crown text-amber-400",
    code: "fa-code text-purple-500",
    cube: "fa-cube text-emerald-500",
  };

  container.innerHTML = pricings.map(p => {
    const hasRemain = p.user_remain_calls > 0;
    const iconClass = iconMap[p.icon_tag] || "fa-robot text-black";

    return `
      <div class="p-4 rounded-2xl border ${hasRemain ? 'border-zinc-200 bg-white hover:border-black' : 'border-dashed border-zinc-300 bg-zinc-50/70'} flex flex-col justify-between space-y-3 transition shadow-sm hover:shadow-md">
        <div class="space-y-2">
          <div class="flex items-start justify-between">
            <div class="w-8 h-8 rounded-xl bg-zinc-100 flex items-center justify-center text-sm shadow-inner">
              <i class="fa-solid ${iconClass}"></i>
            </div>
            <div class="text-right">
              <span class="pill-badge ${hasRemain ? 'pill-badge-green' : 'pill-badge-gray'} text-[10px] font-mono">
                ${hasRemain ? `剩余 ${p.user_remain_calls} 次` : '剩余 0 次'}
              </span>
            </div>
          </div>

          <div>
            <div class="font-extrabold text-sm text-black leading-tight">${p.display_name}</div>
            <div class="text-[10px] text-zinc-400 font-mono mt-0.5">${p.model_key} · ${p.provider}</div>
          </div>

          <p class="text-[11px] text-zinc-500 leading-relaxed line-clamp-2" title="${p.description}">
            ${p.description || '技术部精选旗舰模型，支持代码分析与深度推理。'}
          </p>
        </div>

        <div class="space-y-2 pt-2 border-t border-zinc-100 text-xs">
          <div class="flex items-center justify-between text-[11px]">
            <span class="text-zinc-500">部长设定价格:</span>
            <span class="font-bold text-black font-mono">${p.points_cost}积分 换 ${p.calls_granted}次</span>
          </div>

          <div class="grid grid-cols-2 gap-1.5 pt-1">
            <button type="button" onclick="exchangeModelCalls('${p.model_key}', ${p.id})" class="btn-pill btn-pill-light text-[10px] py-1.5 px-2 font-bold hover:bg-black hover:text-white transition flex items-center justify-center gap-1">
              <i class="fa-solid fa-coins text-amber-500"></i>
              <span>兑换次数</span>
            </button>
            <button type="button" onclick="selectModelForPrompt('${p.model_key}')" class="btn-pill btn-pill-dark text-[10px] py-1.5 px-2 font-bold shadow-sm flex items-center justify-center gap-1 bg-zinc-900 text-white hover:bg-black">
              <i class="fa-solid fa-comment-dots text-sky-300"></i>
              <span>选此提问</span>
            </button>
          </div>
        </div>
      </div>
    `;
  }).join("");
}

// 部员以积分兑换特定模型调用次数
async function exchangeModelCalls(modelKey, pricingId) {
  const target = currentModelPricingsList.find(p => p.id === pricingId || p.model_key === modelKey);
  if (!target) return;

  if (!confirm(`确认消耗 ${target.points_cost} 履职积分兑换【${target.display_name}】的 ${target.calls_granted} 次专属调用吗？`)) {
    return;
  }

  const res = await request("/welfare/exchange-model", {
    method: "POST",
    body: JSON.stringify({ model_key: modelKey, pricing_id: pricingId }),
  });

  if (res && res.ok) {
    const data = await res.json();
    toast("🎉 " + data.message, "success");

    // 更新用户总积分与各模型剩余次数工作台
    if (state.user) {
      state.user.total_score = data.user_total_score;
      localStorage.setItem("xgh_user", JSON.stringify(state.user));
    }
    const scoreBadge = document.getElementById("welfare-user-score-badge");
    if (scoreBadge) scoreBadge.innerText = data.user_total_score;

    loadModelPricingsAndQuotas();
  } else if (res) {
    const err = await res.json();
    toast("兑换失败: " + (err.error || "未知异常"), "error");
  }
}

// 快捷对准模型提问
function selectModelForPrompt(modelKey) {
  const sel = document.getElementById("welfare-relay-model-select");
  if (sel) {
    // 如果下拉框没有，动态插入该选项
    let exists = false;
    for (let i = 0; i < sel.options.length; i++) {
      if (sel.options[i].value === modelKey) {
        sel.selectedIndex = i;
        exists = true;
        break;
      }
    }
    if (!exists) {
      const opt = new Option(modelKey, modelKey);
      sel.add(opt);
      sel.value = modelKey;
    }
  }

  const inp = document.getElementById("welfare-relay-prompt-input");
  if (inp) {
    inp.scrollIntoView({ behavior: "smooth", block: "center" });
    inp.focus();
  }
}

// -----------------------------------------------------------------------------
// 技术部部长专属：自定义模型价格设置窗口 (Modal)
// -----------------------------------------------------------------------------
function openModelPricingModal(pricing = null) {
  const modal = document.getElementById("modal-model-pricing-edit");
  if (!modal) return;
  modal.classList.remove("hidden");

  renderModalPricingExistingList();

  if (pricing) {
    document.getElementById("modal-pricing-title").innerText = `修改模型价格: ${pricing.display_name}`;
    document.getElementById("pricing-inp-id").value = pricing.id;
    document.getElementById("pricing-inp-key").value = pricing.model_key;
    document.getElementById("pricing-inp-key").readOnly = true;
    document.getElementById("pricing-inp-name").value = pricing.display_name;
    document.getElementById("pricing-inp-points").value = pricing.points_cost;
    document.getElementById("pricing-inp-calls").value = pricing.calls_granted;
    document.getElementById("pricing-inp-cost").value = pricing.cost_per_call || 1;
    document.getElementById("pricing-inp-provider").value = pricing.provider || "";
    document.getElementById("pricing-inp-sort").value = pricing.sort_order || 1;
    document.getElementById("pricing-inp-desc").value = pricing.description || "";
    document.getElementById("pricing-chk-enabled").checked = pricing.is_enabled !== false;
  } else {
    resetPricingFormForNew();
  }
}

function closeModelPricingModal() {
  const modal = document.getElementById("modal-model-pricing-edit");
  if (modal) modal.classList.add("hidden");
}

function resetPricingFormForNew() {
  document.getElementById("modal-pricing-title").innerText = "新增自定义 AI 模型价格与兑换规则";
  document.getElementById("pricing-inp-id").value = "";
  document.getElementById("pricing-inp-key").value = "";
  document.getElementById("pricing-inp-key").readOnly = false;
  document.getElementById("pricing-inp-name").value = "";
  document.getElementById("pricing-inp-points").value = 10;
  document.getElementById("pricing-inp-calls").value = 20;
  document.getElementById("pricing-inp-cost").value = 1;
  document.getElementById("pricing-inp-provider").value = "OpenAI/DeepSeek";
  document.getElementById("pricing-inp-sort").value = (currentModelPricingsList.length + 1);
  document.getElementById("pricing-inp-desc").value = "";
  document.getElementById("pricing-chk-enabled").checked = true;
}

function renderModalPricingExistingList() {
  const wrap = document.getElementById("modal-pricing-existing-list");
  if (!wrap) return;

  if (currentModelPricingsList.length === 0) {
    wrap.innerHTML = `<div class="text-zinc-400 text-center py-2">暂无已配置的模型价格</div>`;
    return;
  }

  wrap.innerHTML = currentModelPricingsList.map(p => `
    <div class="flex items-center justify-between p-2 rounded-xl bg-white border border-zinc-100 hover:border-zinc-300 transition">
      <div class="flex items-center gap-2">
        <span class="font-bold text-black">${p.display_name}</span>
        <span class="pill-badge pill-badge-gray text-[9px] font-mono">${p.model_key}</span>
        <span class="text-emerald-700 font-mono font-bold text-[10px]">${p.points_cost}分换${p.calls_granted}次</span>
      </div>
      <div class="flex items-center gap-1.5">
        <button type="button" onclick="openModelPricingModal(${JSON.stringify(p).replace(/"/g, '&quot;')})" class="btn-pill btn-pill-light text-[10px] py-0.5 px-2">编辑</button>
        <button type="button" onclick="deleteModelPricing(${p.id})" class="btn-pill btn-pill-light text-[10px] py-0.5 px-2 text-red-500 hover:border-red-400">删除</button>
      </div>
    </div>
  `).join("");
}

async function handleModelPricingSubmit(e) {
  e.preventDefault();
  const idVal = document.getElementById("pricing-inp-id").value;
  const payload = {
    id: idVal ? parseInt(idVal) : 0,
    model_key: document.getElementById("pricing-inp-key").value.trim(),
    display_name: document.getElementById("pricing-inp-name").value.trim(),
    points_cost: parseInt(document.getElementById("pricing-inp-points").value) || 10,
    calls_granted: parseInt(document.getElementById("pricing-inp-calls").value) || 10,
    cost_per_call: parseInt(document.getElementById("pricing-inp-cost").value) || 1,
    provider: document.getElementById("pricing-inp-provider").value.trim(),
    sort_order: parseInt(document.getElementById("pricing-inp-sort").value) || 1,
    description: document.getElementById("pricing-inp-desc").value.trim(),
    is_enabled: document.getElementById("pricing-chk-enabled").checked,
  };

  const res = await request("/welfare/pricings", {
    method: "POST",
    body: JSON.stringify(payload),
  });

  if (res && res.ok) {
    const data = await res.json();
    toast("✅ " + data.message, "success");
    closeModelPricingModal();
    loadModelPricingsAndQuotas();
  } else if (res) {
    const err = await res.json();
    toast("保存模型价格失败: " + (err.error || "未知异常"), "error");
  }
}

async function deleteModelPricing(id) {
  if (!confirm(`确定要删除此模型的价格规则吗？`)) return;
  const res = await request(`/welfare/pricings/${id}`, { method: "DELETE" });
  if (res && res.ok) {
    toast("已删除该模型价格规则", "success");
    renderModalPricingExistingList();
    loadModelPricingsAndQuotas();
  }
}

async function loadWelfareGateways() {
  const res = await request("/welfare/gateways", { method: "GET" });
  if (!res || !res.ok) return;
  const data = await res.json();
  currentWelfareGateways = data.items || [];

  const listWrap = document.getElementById("welfare-gateways-list");
  if (!listWrap) return;

  if (currentWelfareGateways.length === 0) {
    listWrap.innerHTML = `<div class="text-center py-8 text-zinc-400 text-xs">暂无配置的福利网关，请技术部部长点击右上角添加</div>`;
    return;
  }

  // 默认激活第一个网关
  if (!currentActiveWelfareGateway && currentWelfareGateways.length > 0) {
    currentActiveWelfareGateway = currentWelfareGateways[0];
  } else if (currentActiveWelfareGateway) {
    const updated = currentWelfareGateways.find(g => g.id === currentActiveWelfareGateway.id);
    if (updated) currentActiveWelfareGateway = updated;
  }

  // 更新剩余额度徽标
  const remainQuotaVal = document.getElementById("welfare-remain-quota-val");
  if (remainQuotaVal && currentActiveWelfareGateway) {
    remainQuotaVal.innerText = currentActiveWelfareGateway.user_remain_quota || 0;
  }

  // 更新模型下拉框
  updateWelfareModelSelect();

  const isTechAdmin = (state.user.role === "tech_admin");

  listWrap.innerHTML = currentWelfareGateways.map(gw => {
    const isSelected = currentActiveWelfareGateway && currentActiveWelfareGateway.id === gw.id;
    return `
      <div onclick="selectActiveWelfareGateway(${gw.id})" class="p-4 rounded-2xl border ${isSelected ? 'border-2 border-black bg-zinc-50/90 shadow-md' : 'border-zinc-200 bg-white hover:border-zinc-400'} cursor-pointer transition space-y-2 text-xs">
        <div class="flex items-center justify-between">
          <div class="font-black text-black text-sm flex items-center gap-1.5">
            <span class="w-2 h-2 rounded-full ${gw.is_active ? 'bg-emerald-500' : 'bg-zinc-300'}"></span>
            <span>${gw.gateway_name}</span>
          </div>
          <span class="pill-badge pill-badge-dark text-[9px] font-mono">${gw.point_cost_per_call} 积分/次</span>
        </div>

        <div class="text-zinc-500 font-mono text-[11px] truncate" title="${gw.base_url}">
          <i class="fa-solid fa-link mr-1 text-zinc-400"></i> ${gw.base_url}
        </div>

        <div class="flex flex-wrap items-center gap-1 pt-1">
          <span class="text-[10px] text-zinc-400">已识别模型:</span>
          ${gw.recognized_models.slice(0, 4).map(m => `<span class="px-1.5 py-0.5 rounded bg-zinc-100 font-mono text-[10px] text-zinc-700">${m}</span>`).join("")}
          ${gw.recognized_models.length > 4 ? `<span class="text-[10px] text-zinc-400 font-mono">+${gw.recognized_models.length - 4}</span>` : ''}
        </div>

        <div class="flex items-center justify-between pt-2 border-t border-zinc-100 text-[11px]">
          <span class="text-zinc-500">配置人: <span class="font-bold text-black">${gw.owner_name}</span> · 我的额度: <b class="text-emerald-600">${gw.user_remain_quota || 0} 次</b></span>
          <div class="space-x-1">
            ${isTechAdmin ? `
              <button onclick="event.stopPropagation(); probeGatewayModels(${gw.id})" class="btn-pill btn-pill-light text-[10px] py-0.5 px-2 hover:border-black font-semibold" title="探测上游 /models 识别模型">
                <i class="fa-solid fa-radar mr-0.5 text-sky-500"></i> 探测模型
              </button>
              <button onclick="event.stopPropagation(); openWelfareGatewayModal(${JSON.stringify(gw).replace(/"/g, '&quot;')})" class="btn-pill btn-pill-light text-[10px] py-0.5 px-2 font-semibold">
                编辑
              </button>
            ` : ''}
          </div>
        </div>
      </div>
    `;
  }).join("");
}

function selectActiveWelfareGateway(id) {
  const target = currentWelfareGateways.find(g => g.id === id);
  if (target) {
    currentActiveWelfareGateway = target;
    const remainQuotaVal = document.getElementById("welfare-remain-quota-val");
    if (remainQuotaVal) remainQuotaVal.innerText = target.user_remain_quota || 0;
    updateWelfareModelSelect();
    resetWelfareRelayChat();
    loadWelfareGateways();
  }
}

function updateWelfareModelSelect() {
  const sel = document.getElementById("welfare-relay-model-select");
  if (!sel || !currentActiveWelfareGateway) return;

  const models = currentActiveWelfareGateway.recognized_models || ["gpt-4o-mini", "deepseek-chat"];
  sel.innerHTML = models.map(m => `
    <option value="${m}" ${m === currentActiveWelfareGateway.default_model ? 'selected' : ''}>${m}</option>
  `).join("");
}

function openWelfareGatewayModal(gw = null) {
  const modal = document.getElementById("modal-welfare-gateway-edit");
  if (!modal) return;
  modal.classList.remove("hidden");

  const idInp = document.getElementById("welfare-gw-id");
  const nameInp = document.getElementById("welfare-gw-name");
  const urlInp = document.getElementById("welfare-gw-url");
  const keyInp = document.getElementById("welfare-gw-key");
  const modelInp = document.getElementById("welfare-gw-model");
  const pointsInp = document.getElementById("welfare-gw-points");

  if (gw) {
    if (idInp) idInp.value = gw.id;
    if (nameInp) nameInp.value = gw.gateway_name;
    if (urlInp) urlInp.value = gw.base_url;
    if (keyInp) {
      keyInp.value = "";
      // 浏览器拿不到明文密钥，编辑时只能凭脱敏串确认当前挂的是哪一把。
      keyInp.placeholder = gw.has_key
        ? `已存密钥 ${gw.key_mask || "****"}，留空则保持不变`
        : "尚未配置密钥，输入 sk 开头的密钥后保存";
    }
    if (modelInp) modelInp.value = gw.default_model || "gpt-4o-mini";
    if (pointsInp) pointsInp.value = gw.point_cost_per_call || 2;
  } else {
    if (idInp) idInp.value = "";
    if (nameInp) nameInp.value = "技术部 DeepSeek / OpenAI 福利中继站";
    if (urlInp) urlInp.value = "https://api.openai.com/v1";
    if (keyInp) {
      keyInp.value = "";
      keyInp.placeholder = "输入 sk 开头的密钥，留空则保持现有密钥";
    }
    if (modelInp) modelInp.value = "gpt-4o-mini";
    if (pointsInp) pointsInp.value = 2;
  }
}

function closeWelfareGatewayModal() {
  const modal = document.getElementById("modal-welfare-gateway-edit");
  if (modal) modal.classList.add("hidden");
}

async function handleWelfareGatewaySubmit(e) {
  e.preventDefault();
  const id = document.getElementById("welfare-gw-id").value;
  const payload = {
    id: id ? parseInt(id) : 0,
    gateway_name: document.getElementById("welfare-gw-name").value.trim(),
    base_url: document.getElementById("welfare-gw-url").value.trim(),
    api_key: document.getElementById("welfare-gw-key").value.trim(),
    default_model: document.getElementById("welfare-gw-model").value.trim(),
    point_cost_per_call: parseInt(document.getElementById("welfare-gw-points").value) || 2,
    is_active: true,
  };

  const res = await request("/welfare/gateways", {
    method: "POST",
    body: JSON.stringify(payload),
  });

  if (res && res.ok) {
    const data = await res.json();
    toast("✅ " + data.message, "success");
    closeWelfareGatewayModal();
    loadWelfareGateways();
  } else if (res) {
    const err = await res.json();
    toast("保存网关失败: " + (err.error || "未知原因"), "error");
  }
}

// 自动识别上游模型
async function probeGatewayModels(id) {
  const res = await request(`/welfare/gateways/${id}/probe-models`, { method: "POST" });
  if (res && res.ok) {
    const data = await res.json();
    toast("🤖 " + data.message + "\n已自动识别为可用模型列表并更新！", "success");
    loadWelfareGateways();
  } else if (res) {
    const err = await res.json();
    toast("模型探测失败: " + (err.error || "未知原因"), "error");
  }
}

// 部员使用积分兑换 AI 高阶额度
async function exchangeWelfareQuota(packLevel) {
  if (!currentActiveWelfareGateway) {
    toast("请先在左侧选择一个有效的福利网关通道！", "warning");
    return;
  }

  const costMap = { 1: 10, 2: 20, 3: 50 };
  const addMap = { 1: 5, 2: 12, 3: 35 };
  const cost = costMap[packLevel];
  const add = addMap[packLevel];

  if (!confirm(`确认使用 ${cost} 查寝积分兑换 ${add} 次高阶 AI 模型调用额度吗？`)) {
    return;
  }

  const res = await request(`/welfare/gateways/${currentActiveWelfareGateway.id}/exchange`, {
    method: "POST",
    body: JSON.stringify({
      gateway_id: currentActiveWelfareGateway.id,
      exchange_pack: packLevel,
    }),
  });

  if (res && res.ok) {
    const data = await res.json();
    toast(data.message, "info");

    // 更新当前用户积分与额度
    if (state.user) {
      state.user.total_score = data.user_total_score;
      localStorage.setItem("xgh_user", JSON.stringify(state.user));
    }
    const scoreBadge = document.getElementById("welfare-user-score-badge");
    if (scoreBadge) scoreBadge.innerText = data.user_total_score;

    loadWelfareGateways();
  } else if (res) {
    const err = await res.json();
    toast(err.error || "兑换异常，请稍后重试", "error");
  }
}

// 透传终端的多轮上下文只存在浏览器里，服务端每轮只保留最近若干轮，切换通道即清空。
let welfareRelayHistory = [];

function welfareRelayGreetingHtml() {
  return `
    <div class="p-3.5 rounded-2xl bg-white border border-zinc-200 text-zinc-700 space-y-1 leading-relaxed shadow-sm">
      <div class="font-black text-black flex items-center gap-1.5 text-xs">
        <i class="fa-solid fa-cube text-amber-500"></i> 技术部福利中枢
      </div>
      <p>同学您好！这里是学管会技术部部署的真实 AI 透传通道：每一次回答都由上游模型当场生成，调用失败会直接告诉您原因，不会用模板话术顶替。每次调用消耗您兑换的可用次数。</p>
    </div>
  `;
}

function resetWelfareRelayChat() {
  welfareRelayHistory = [];
  const chatWin = document.getElementById("welfare-relay-chat-window");
  if (chatWin) chatWin.innerHTML = welfareRelayGreetingHtml();
}

// 发送安全透传请求 (密钥不落地)
async function handleWelfareRelayChat(e) {
  e.preventDefault();
  if (!currentActiveWelfareGateway) {
    toast("请在左侧选择一个福利网关通道", "warning");
    return;
  }

  const inp = document.getElementById("welfare-relay-prompt-input");
  const prompt = inp.value.trim();
  if (!prompt) return;

  const modelSel = document.getElementById("welfare-relay-model-select");
  const selectedModel = modelSel ? modelSel.value : "";

  const chatWin = document.getElementById("welfare-relay-chat-window");
  // 用户与模型的所有文本都必须转义后入 DOM：模型回复是上游返回的远端内容
  chatWin.innerHTML += `
    <div class="p-3 rounded-2xl bg-black text-white ml-6 space-y-1 text-xs">
      <div class="font-bold text-[10px] text-zinc-400">我 (${escapeHtml(state.user.real_name)})：</div>
      <p class="leading-relaxed whitespace-pre-wrap">${escapeHtml(prompt)}</p>
    </div>
  `;
  chatWin.scrollTop = chatWin.scrollHeight;
  inp.value = "";

  const btn = document.getElementById("btn-welfare-relay-send");
  btn.disabled = true;
  btn.innerHTML = `<i class="fa-solid fa-spinner animate-spin"></i> <span>服务器安全反向透传中...</span>`;

  const res = await request("/welfare/chat-relay", {
    method: "POST",
    body: JSON.stringify({
      gateway_id: currentActiveWelfareGateway.id,
      model: selectedModel,
      prompt: prompt,
      history: welfareRelayHistory.slice(-10),
    }),
  });

  btn.disabled = false;
  btn.innerHTML = `<i class="fa-solid fa-paper-plane text-xs"></i> <span>安全透传发送</span>`;

  if (!res) return;

  const data = await res.json().catch(() => ({}));

  if (!res.ok) {
    // 失败时不写入上下文：这一轮没有产生计费，用户重试不应带上半截对话
    const detail = data.error || `透传服务异常 ${res.status}`;
    chatWin.innerHTML += `
      <div class="p-3.5 rounded-2xl bg-red-50 border border-red-200 text-red-800 mr-6 space-y-1 text-xs">
        <div class="font-black flex items-center gap-1.5 text-[11px] text-red-700">
          <i class="fa-solid fa-plug-circle-xmark"></i> 本轮调用失败${data.ai_status === "failed" ? "（未扣除次数）" : ""}
        </div>
        <div class="leading-relaxed whitespace-pre-wrap">${escapeHtml(detail)}</div>
      </div>
    `;
    chatWin.scrollTop = chatWin.scrollHeight;
    toast("调用失败: " + detail, "error", 6000);
    return;
  }

  chatWin.innerHTML += `
    <div class="p-3.5 rounded-2xl bg-white border border-zinc-200 text-zinc-800 mr-6 space-y-1 text-xs shadow-sm">
      <div class="font-bold text-black flex items-center justify-between text-[11px] pb-1 border-b border-zinc-100">
        <span class="flex items-center gap-1.5"><i class="fa-solid fa-microchip text-sky-600"></i> ${escapeHtml(data.model || selectedModel || "")}</span>
        <span class="text-[10px] font-mono text-zinc-400">通道: ${escapeHtml(data.gateway_name || "")} · ${data.duration_ms || 0}ms · 耗 ${data.is_owner ? "0（配置人自用）" : (data.cost_per_call || 1) + " 次"}</span>
      </div>
      <div class="leading-relaxed whitespace-pre-wrap pt-1">${escapeHtml(data.reply || "")}</div>
    </div>
  `;
  chatWin.scrollTop = chatWin.scrollHeight;

  welfareRelayHistory.push({ role: "user", content: prompt });
  welfareRelayHistory.push({ role: "assistant", content: data.reply });

  // 刷新额度与各模型剩余次数工作台
  if (data.remain_calls !== undefined) {
    const remainEl = document.getElementById("welfare-remain-quota-val");
    if (remainEl) remainEl.innerText = data.remain_calls;
  }
  // 实时重新加载各模型剩余次数卡片
  loadModelPricingsAndQuotas();
}

// =============================================================================
// 8. 宣传部专属：随机插画素材灵感工坊 & 播音组新闻广播看板
// =============================================================================
let currentPublicityTag = "anime";
let cachedBroadcastScript = "";

async function loadPublicityGallery() {
  const grid = document.getElementById("publicity-gallery-grid");
  if (!grid) return;
  grid.innerHTML = `<div class="p-8 text-center text-zinc-400 text-xs col-span-full"><i class="fa-solid fa-spinner fa-spin mr-1.5"></i> 正在调用素材灵感 API...</div>`;

  const res = await request(`/publicity/images?tag=${currentPublicityTag}&limit=6`, { method: "GET" });
  if (!res || !res.ok) {
    grid.innerHTML = `<div class="p-8 text-center text-zinc-400 text-xs col-span-full">获取灵感插画异常，请稍后刷新重试</div>`;
    return;
  }

  const data = await res.json();
  const items = data.items || [];
  if (items.length === 0) {
    grid.innerHTML = `<div class="p-8 text-center text-zinc-400 text-xs col-span-full">当前标签暂无插画</div>`;
    return;
  }

  grid.innerHTML = items.map(img => `
    <div class="glass-card overflow-hidden rounded-3xl border border-zinc-200 bg-white hover:shadow-xl transition-all duration-300 group">
      <div class="relative h-48 sm:h-56 bg-zinc-100 overflow-hidden">
        <img src="${img.image_url}" alt="${img.title}" class="w-full h-full object-cover group-hover:scale-105 transition-all duration-500" loading="lazy">
        <div class="absolute top-3 right-3">
          <span class="pill-badge pill-badge-dark text-[10px] bg-black/70 backdrop-blur-md text-white">${img.tag}</span>
        </div>
      </div>
      <div class="p-4 space-y-2">
        <div class="flex items-center justify-between">
          <h4 class="font-extrabold text-sm text-black truncate">${img.title}</h4>
          <span class="text-[10px] text-zinc-400 font-mono">1920x1080</span>
        </div>
        <p class="text-xs text-zinc-400 line-clamp-2 leading-relaxed">${img.description || '学管会宣传部专属灵感创作素材'}</p>
        <div class="pt-2 border-t border-zinc-100 flex items-center justify-between text-xs">
          <a href="${img.image_url}" target="_blank" class="text-black font-bold underline text-[11px] hover:opacity-70 flex items-center gap-1">
            <i class="fa-solid fa-arrow-up-right-from-square text-[10px]"></i> 查看原图
          </a>
          <button onclick="copyToClipboard('${img.image_url}', '插画链接已复制！')" class="btn-pill btn-pill-light text-[10px] py-1 px-2.5">
            复制链接
          </button>
        </div>
      </div>
    </div>
  `).join("");
}

function switchGalleryTag(tag) {
  currentPublicityTag = tag;
  const label = document.getElementById("gallery-current-tag-label");
  if (label) label.innerText = `当前标签: ${tag}`;

  const pills = document.querySelectorAll("#gallery-tag-pills .nav-pill-item");
  pills.forEach(p => {
    if (p.getAttribute("data-tag") === tag) {
      p.classList.add("active");
    } else {
      p.classList.remove("active");
    }
  });

  loadPublicityGallery();
}

function refreshPublicityGallery() {
  loadPublicityGallery();
}

// 播音组新闻与讲稿看板
async function loadBroadcastNewsView() {
  searchBroadcastNews();
  loadBroadcastRankPush();
}

async function searchBroadcastNews() {
  const inp = document.getElementById("broadcast-news-kw-input");
  const kw = inp ? inp.value.trim() : "";
  const list = document.getElementById("broadcast-news-results-list");
  if (!list) return;

  list.innerHTML = `<div class="text-xs text-zinc-400 p-4 text-center">正在检索校园新闻...</div>`;
  const res = await request(`/publicity/broadcast-news?keyword=${encodeURIComponent(kw)}`, { method: "GET" });
  if (!res || !res.ok) {
    list.innerHTML = `<div class="text-xs text-zinc-400 p-4 text-center">检索异常</div>`;
    return;
  }

  const data = await res.json();
  const items = data.items || [];
  if (items.length === 0) {
    list.innerHTML = `<div class="text-xs text-zinc-400 p-4 text-center">未检索到与关键词匹配的新闻</div>`;
    return;
  }

  list.innerHTML = items.map(n => `
    <div class="p-4 rounded-2xl bg-zinc-50 border border-zinc-200/80 space-y-2 hover:border-black transition">
      <div class="flex items-center justify-between">
        <span class="font-extrabold text-sm text-black">${n.title}</span>
        <span class="pill-badge pill-badge-gray text-[10px]">${n.category}</span>
      </div>
      <p class="text-xs text-zinc-600 leading-relaxed">${n.content}</p>
      <div class="flex items-center justify-between text-[11px] text-zinc-400 pt-1 border-t border-zinc-100">
        <span>来源：${n.source || '校广播台'}</span>
        <button onclick="copyToClipboard('${n.content.replace(/'/g, "\\'")}', '新闻播音素材已复制！')" class="text-black font-semibold underline hover:opacity-75">
          选用为播音素材
        </button>
      </div>
    </div>
  `).join("");
}

async function loadBroadcastRankPush() {
  const contentEl = document.getElementById("broadcast-script-content");
  if (!contentEl) return;
  contentEl.innerHTML = `<span class="text-zinc-400"><i class="fa-solid fa-spinner fa-spin mr-1"></i> 正在自动统计全员表现与生成播报词...</span>`;

  const res = await request("/publicity/broadcast-rank-push?top=3&bottom=2", { method: "GET" });
  if (!res || !res.ok) {
    contentEl.innerText = "生成通报播音稿失败，请稍后重试。";
    return;
  }

  const data = await res.json();
  cachedBroadcastScript = data.speech_script || "";
  contentEl.innerText = cachedBroadcastScript;
}

function copyBroadcastScript() {
  if (!cachedBroadcastScript) {
    toast("请先点击生成播音讲稿", "warning");
    return;
  }
  copyToClipboard(cachedBroadcastScript, "播音讲稿已成功复制到剪贴板！可直接送入广播室宣读。");
}

function copyToClipboard(text, successMsg = "已复制到剪贴板") {
  if (navigator.clipboard) {
    navigator.clipboard.writeText(text).then(() => toast(successMsg, "success"));
  } else {
    const ta = document.createElement("textarea");
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    document.body.removeChild(ta);
    toast(successMsg, "success");
  }
}

// =============================================================================
// 9. 用户个人安全设置 (修改账号用户名、真实姓名、联系电话、密码)
// =============================================================================
function loadSecuritySettings() {
  if (!state.user) return;
  const usernameInp = document.getElementById("sec-inp-username");
  const realNameInp = document.getElementById("sec-inp-realname");
  const phoneInp = document.getElementById("sec-inp-phone");
  const roleInp = document.getElementById("sec-inp-role");
  const oldPwdInp = document.getElementById("sec-inp-oldpwd");
  const newPwdInp = document.getElementById("sec-inp-newpwd");

  if (usernameInp) usernameInp.value = state.user.username || "";
  if (realNameInp) realNameInp.value = state.user.real_name || "";
  if (phoneInp) phoneInp.value = state.user.phone || "";
  if (roleInp) roleInp.value = `${state.user.role} (${state.user.department || '学管会'})`;
  if (oldPwdInp) oldPwdInp.value = "";
  if (newPwdInp) newPwdInp.value = "";
}

async function handleSaveSecuritySettings(e) {
  e.preventDefault();
  const username = document.getElementById("sec-inp-username").value.trim();
  const realname = document.getElementById("sec-inp-realname").value.trim();
  const phone = document.getElementById("sec-inp-phone").value.trim();
  const oldPassword = document.getElementById("sec-inp-oldpwd").value;
  const newPassword = document.getElementById("sec-inp-newpwd").value;

  if (newPassword && newPassword.length < 6) {
    toast("新密码长度必须至少为 6 位！", "warning");
    return;
  }

  if (newPassword && !oldPassword) {
    toast("修改密码时，必须输入当前原密码进行安全核验！", "warning");
    return;
  }

  const btn = document.getElementById("btn-save-security-settings");
  if (btn) {
    btn.disabled = true;
    btn.innerHTML = `<i class="fa-solid fa-spinner fa-spin mr-1.5"></i> 正在安全更新凭据...`;
  }

  const res = await request("/auth/security-settings", {
    method: "PUT",
    body: JSON.stringify({
      username: username,
      real_name: realname,
      phone: phone,
      old_password: oldPassword,
      new_password: newPassword,
    }),
  });

  if (btn) {
    btn.disabled = false;
    btn.innerHTML = `<i class="fa-solid fa-floppy-disk text-xs"></i> <span>保存并更新安全设置</span>`;
  }

  if (res && res.ok) {
    const data = await res.json();
    toast("🔒 " + data.message, "info");

    // 更新本地持久化用户信息与令牌
    state.user = data.user;
    if (data.token) {
      state.token = data.token;
      localStorage.setItem("xgh_token", data.token);
    }
    localStorage.setItem("xgh_user", JSON.stringify(data.user));

    // 重新渲染侧边栏用户卡片
    renderUserSlot();
    loadSecuritySettings();
  } else if (res) {
    const err = await res.json();
    toast("更新安全设置失败: " + (err.error || "未知异常"), "error");
  }
}

