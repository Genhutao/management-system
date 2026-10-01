package middleware

import (
	"strings"
	"testing"
)

// 素材工坊改成同源供图后，浏览器要多打一条四段深度的路径
// （/api/v1/publicity/images/file/<64位哈希>.webp）。策略里写的是 /api/v1/publicity/*，
// 通配符能不能吃到这么深，决定图片是不是只有部长看得到、部员全裂图。
// 表现只在登录后的界面上才看得见，所以在策略层先钉住。
func TestCasbinPublicityGalleryRoutes(t *testing.T) {
	if err := newCasbinEnforcer(t); err != nil {
		t.Fatalf("初始化 Casbin 引擎失败: %v", err)
	}

	hash := strings.Repeat("a", 64)
	routes := []string{
		"/api/v1/publicity/images",
		"/api/v1/publicity/gallery/random-images",
		"/api/v1/publicity/images/categories",
		"/api/v1/publicity/images/file/" + hash + ".webp",
	}

	for _, role := range []string{"member", "minister", "tech_admin"} {
		for _, path := range routes {
			ok, err := Enforcer.Enforce("role:"+role, path, "GET")
			if err != nil {
				t.Fatalf("策略求值出错 (%s): %v", path, err)
			}
			if !ok {
				t.Errorf("角色 %s 应可读取素材工坊 %s —— 深层路径没被通配符覆盖", role, path)
			}
		}
	}

	// 反向：宿管与只读岗没有宣传素材需求，不该被通配符顺手放进来
	for _, role := range []string{"dorm_manager", "viewer_export", "stranger"} {
		for _, path := range routes {
			if ok, _ := Enforcer.Enforce("role:"+role, path, "GET"); ok {
				t.Errorf("角色 %s 不应能读取 %s", role, path)
			}
		}
	}
}
