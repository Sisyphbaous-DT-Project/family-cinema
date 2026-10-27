package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"media-server/internal/anime/airtime"
	"media-server/internal/anime/dl"
	"media-server/internal/anime/follow"
	"media-server/internal/anime/resolve"
)

// followView 追番 + 播出时间
type followView struct {
	follow.Follow
	Air *airtime.View `json:"air,omitempty"`
}

func (a *API) viewOf(f follow.Follow, now time.Time) followView {
	v := followView{Follow: f}
	if e, ok := a.air.Get(f.ID); ok {
		av := e.View(now)
		v.Air = &av
	}
	return v
}

// GET /api/anime/follows  追番清单（新追的在前）
func (a *API) handleFollows(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	list := a.follow.List()
	out := make([]followView, 0, len(list))
	for _, f := range list {
		out = append(out, a.viewOf(f, now))
	}
	latest, _, _ := a.follow.Events(-1)
	writeJSON(w, map[string]any{"items": out, "latestEvent": latest})
}

type followReq struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Name  string `json:"name"`
	Cover string `json:"cover"`
	Rule  string `json:"rule"`
	Src   string `json:"src"`
}

// POST /api/anime/follows  追番；带上 rule/src 就以这个片源查更新
func (a *API) handleFollowAdd(w http.ResponseWriter, r *http.Request) {
	var req followReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.ID <= 0 || strings.TrimSpace(req.Title) == "" {
		writeErr(w, http.StatusBadRequest, "追番信息不完整")
		return
	}
	f := follow.Follow{ID: req.ID, Title: strings.TrimSpace(req.Title), Name: strings.TrimSpace(req.Name), Cover: req.Cover}
	if a.rules.Get(req.Rule) != nil && req.Src != "" {
		f.Rule, f.Src, f.SrcName = req.Rule, req.Src, req.Rule
	}
	writeJSON(w, a.viewOf(a.follow.Add(f), time.Now()))
}

// DELETE /api/anime/follows/{id}  取消追番
func (a *API) handleFollowRemove(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	writeJSON(w, map[string]bool{"ok": a.follow.Remove(id)})
}

// POST /api/anime/follows/{id}/source  记录用户选择的片源（没追的番忽略）
func (a *API) handleFollowSource(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	var req followReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || a.rules.Get(req.Rule) == nil || req.Src == "" {
		writeErr(w, http.StatusBadRequest, "片源信息不完整")
		return
	}
	writeJSON(w, map[string]bool{"ok": a.follow.SetSource(id, req.Rule, req.Src, req.Rule)})
}

// GET /api/anime/follows/events?after=N  手机 App 定时来取更新消息；
// 不带 after（或 after<0）只回最新编号，App 第一次来记个起点，不补发旧消息
func (a *API) handleFollowEvents(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil {
		after = -1
	}
	latest, evs, n := a.follow.Events(after)
	writeJSON(w, map[string]any{"latest": latest, "events": evs, "follows": n})
}

// GET /api/anime/airtime?ids=1,2,3  这几部番几点更新（查不到的不出现在结果里）
func (a *API) handleAirtime(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	out := map[string]airtime.View{}
	for i, s := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if i >= 200 {
			break
		}
		id, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		if e, ok := a.air.Get(id); ok {
			out[strconv.Itoa(id)] = e.View(now)
		}
	}
	writeJSON(w, out)
}

type sizeResult struct {
	Status string `json:"status"` // ok | need-worker | fail
	dl.Size
	Reason string `json:"reason,omitempty"`
}

// GET /api/anime/size?rule=&ep=&ref=  下载前量一集有多大，结果记 6 小时。
// mp4 问服务器（准确）；m3u8 每部番第一集把分段大小挨个问一遍（准确），记下这部番每秒多少字节，
// 后面的集只读清单时长乘码率（一个请求，误差几个百分点）。
// 前端退出多选会断开请求，这里照样量完记下，下次再进多选直接用。
func (a *API) handleSize(w http.ResponseWriter, r *http.Request) {
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
	ref := r.URL.Query().Get("ref")
	series := rule.Name + "|" + ref // 同一部番（同一个片源作品）共用码率
	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") {
		ref = ""
	}
	key := rule.Name + "|" + ep
	if v, ok := a.sizeCache.get(key); ok {
		writeJSON(w, v)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 90*time.Second)
	defer cancel()
	t, kind, err := a.resolver.ResolveTarget(ctx, rule, ep, ref)
	switch {
	case errors.Is(err, resolve.ErrNeedWorker):
		writeJSON(w, sizeResult{Status: "need-worker", Reason: err.Error()})
		return
	case err != nil:
		writeJSON(w, sizeResult{Status: "fail", Reason: err.Error()})
		return
	}
	fetch := func(ctx context.Context, rawURL, rng string) (*http.Response, error) {
		return a.Fetch(ctx, t, rawURL, rng)
	}
	rate, _ := a.rateCache.get(series)
	sz, err := dl.Measure(ctx, fetch, t.URL, kind, t.AdBlock, rate)
	if err != nil {
		writeJSON(w, sizeResult{Status: "fail", Reason: err.Error()})
		return
	}
	if sz.Exact && sz.Seconds > 0 {
		a.rateCache.set(series, float64(sz.Bytes)/sz.Seconds)
	}
	res := sizeResult{Status: "ok", Size: sz}
	a.sizeCache.set(key, res)
	writeJSON(w, res)
}
