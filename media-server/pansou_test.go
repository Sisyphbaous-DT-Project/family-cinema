package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPansouSearchProxyAndDisabledFeature(t *testing.T) {
	old := cfg
	t.Cleanup(func() { cfg = old })
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/search" || r.Method != http.MethodPost {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("client credentials forwarded to PanSou")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "测试影片") {
			t.Errorf("search body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"total":1,"merged_by_type":{"quark":[{"url":"https://example.com/video"}]}}}`)
	}))
	defer upstream.Close()
	cfg = defaultConfig()
	cfg.Pansou.URL = upstream.URL
	mux := http.NewServeMux()
	mountPansou(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/pansou/search?target=http://example.org/", strings.NewReader(`{"kw":"测试影片","src":"plugin"}`))
	req.Header.Set("Authorization", "private-value")
	req.Header.Set("Cookie", "private=value")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":1`) || requests != 1 {
		t.Fatalf("proxy response = %d %s, requests=%d", rec.Code, rec.Body.String(), requests)
	}
	cfg.Pansou.Enabled = false
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/features", nil))
	var feature map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &feature); err != nil || feature["pansou"] {
		t.Fatalf("disabled feature response: %s, err=%v", rec.Body.String(), err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pansou/search", strings.NewReader(`{"kw":"影片"}`)))
	if rec.Code != http.StatusServiceUnavailable || requests != 1 {
		t.Fatalf("disabled search = %d, requests=%d", rec.Code, requests)
	}
}

func TestPansouSearchRejectsRedirectAndOversizeRequest(t *testing.T) {
	old := cfg
	t.Cleanup(func() { cfg = old })
	destinationRequests := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationRequests++
	}))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	cfg = defaultConfig()
	cfg.Pansou.URL = upstream.URL
	mux := http.NewServeMux()
	mountPansou(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pansou/search", strings.NewReader(`{"kw":"影片"}`)))
	if rec.Code != http.StatusTemporaryRedirect || destinationRequests != 0 || rec.Header().Get("Location") != "" {
		t.Fatalf("redirect escaped configured endpoint: status=%d requests=%d", rec.Code, destinationRequests)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pansou/search", strings.NewReader(strings.Repeat("x", (128<<10)+1))))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d", rec.Code)
	}
}
