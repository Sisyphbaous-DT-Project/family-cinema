package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"media-server/internal/anime/engine"
	"media-server/internal/anime/sniff"
)

// GET /api/anime/rules
func (a *API) handleRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.rules.Infos())
}

// GET /api/anime/source/search?rule=&q=
// 单个站点失败不是 HTTP 错误：返回 200 + error 字段，前端按站点显示状态即可。
func (a *API) handleSourceSearch(w http.ResponseWriter, r *http.Request) {
	rule := a.rules.Get(r.URL.Query().Get("rule"))
	if rule == nil {
		writeErr(w, http.StatusNotFound, "没有这个规则")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, map[string]any{"items": []engine.Item{}})
		return
	}
	key := rule.Name + "|" + q
	if items, ok := a.searchCache.get(key); ok {
		writeJSON(w, map[string]any{"items": items})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	items, err := a.engine.Search(ctx, rule, q)
	if err != nil {
		writeJSON(w, map[string]any{"items": []engine.Item{}, "error": err.Error()})
		return
	}
	if items == nil {
		items = []engine.Item{}
	}
	a.searchCache.set(key, items)
	writeJSON(w, map[string]any{"items": items})
}

// GET /api/anime/source/chapters?rule=&src=
// src 是搜索结果给的来源：详情页地址，或接口模式规则给的作品编号（由引擎按规则补成地址/代入接口）。
func (a *API) handleSourceChapters(w http.ResponseWriter, r *http.Request) {
	rule := a.rules.Get(r.URL.Query().Get("rule"))
	if rule == nil {
		writeErr(w, http.StatusNotFound, "没有这个规则")
		return
	}
	src := strings.TrimSpace(r.URL.Query().Get("src"))
	if src == "" {
		writeErr(w, http.StatusBadRequest, "缺少 src")
		return
	}
	key := rule.Name + "|" + src
	if roads, ok := a.chaptersCache.get(key); ok {
		writeJSON(w, map[string]any{"roads": roads})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	roads, err := a.engine.Chapters(ctx, rule, src)
	if err != nil {
		writeJSON(w, map[string]any{"roads": []engine.Road{}, "error": err.Error()})
		return
	}
	if roads == nil {
		roads = []engine.Road{}
	}
	a.chaptersCache.set(key, roads)
	writeJSON(w, map[string]any{"roads": roads})
}

// GET /api/anime/resolve?rule=&ep=&ref=  解析某一集的播放地址（走嗅探时最长约 25 秒）
func (a *API) handleResolve(w http.ResponseWriter, r *http.Request) {
	rule := a.rules.Get(r.URL.Query().Get("rule"))
	if rule == nil {
		writeErr(w, http.StatusNotFound, "没有这个规则")
		return
	}
	ep := r.URL.Query().Get("ep")
	if !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
		writeErr(w, http.StatusBadRequest, "ep 必须是播放页地址")
		return
	}
	// ref 是前端传来的作品来源，接口模式规则下可能只是个编号，不能当 Referer 发出去
	ref := r.URL.Query().Get("ref")
	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") {
		ref = ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	writeJSON(w, a.resolver.Resolve(ctx, rule, ep, ref))
}

// GET /api/anime/worker/poll?id=<设备id>  手机助手长轮询取任务，没有任务 204
func (a *API) handleWorkerPoll(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少设备 id")
		return
	}
	job := a.hub.Poll(r.Context(), id, 25*time.Second)
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, job)
}

// POST /api/anime/worker/result  手机回报嗅探结果
func (a *API) handleWorkerResult(w http.ResponseWriter, r *http.Request) {
	var res sniff.Result
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&res); err != nil || res.JobID == "" {
		writeErr(w, http.StatusBadRequest, "回报内容不对")
		return
	}
	// 代取地址只信对端实际连接地址，不能让回报内容指定一个任意主机
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		res.RelayAddr = host
	}
	writeJSON(w, map[string]bool{"ok": a.hub.Report(res)})
}

// GET /api/anime/worker/status  在线的解析助手数量
func (a *API) handleWorkerStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]int{"online": a.hub.Online()})
}
