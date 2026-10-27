package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyFrontendServed(t *testing.T) {
	srv := NewServer()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/legacy", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/legacy/" {
		t.Errorf("/legacy 应跳转到 /legacy/: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/legacy/", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `href="style.css"`) || !strings.Contains(body, `src="app.js"`) {
		t.Fatalf("旧前端首页应引用相对路径的 style.css/app.js: %d", rec.Code)
	}

	for _, p := range []string{"/legacy/app.js", "/legacy/style.css"} {
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 || rec.Body.Len() == 0 {
			t.Errorf("%s 应能取到: %d", p, rec.Code)
		}
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/legacy/../main.go", nil))
	if rec.Code == 200 && strings.Contains(rec.Body.String(), "package main") {
		t.Error("不应能越权读到源码")
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/legacy/secret.txt", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("白名单之外的文件应 404: %d", rec.Code)
	}
}
