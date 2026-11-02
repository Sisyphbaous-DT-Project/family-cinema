package api

import (
	"context"
	"net/http"
	"time"

	"media-server/internal/anime/rulesync"
)

// GET /api/anime/rules/sync  规则自动更新的情况（没开自动更新时 enabled=false）
func (a *API) handleRuleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if a.ruleSync == nil {
		writeJSON(w, rulesync.Status{})
		return
	}
	writeJSON(w, a.ruleSync.Status())
}

// POST /api/anime/rules/sync  马上检查一次，跑完才返回（一般几秒）。
// 页面关掉也让这一轮跑完，免得规则下到一半作废。
func (a *API) handleRuleSyncNow(w http.ResponseWriter, r *http.Request) {
	if a.ruleSync == nil {
		writeErr(w, http.StatusNotFound, "规则自动更新没开")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	writeJSON(w, a.ruleSync.Check(ctx))
}
