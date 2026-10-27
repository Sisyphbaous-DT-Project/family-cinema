package api

import (
	"net/http"
	"strconv"
	"strings"

	"media-server/internal/anime/bangumi"
)

// proxyImages 把封面地址换成走盒子图片代理的地址（缓存里存的是上游原始数据，这里改的是副本）
func proxyImages(im bangumi.Images) bangumi.Images {
	return bangumi.Images{
		Large:  bangumi.ProxyURL(im.Large),
		Common: bangumi.ProxyURL(im.Common),
		Medium: bangumi.ProxyURL(im.Medium),
		Small:  bangumi.ProxyURL(im.Small),
		Grid:   bangumi.ProxyURL(im.Grid),
	}
}

func fixCards(cards []bangumi.Card) {
	for i := range cards {
		cards[i].Images = proxyImages(cards[i].Images)
	}
}

// GET /api/anime/hot?offset=&limit= 近两季热门
func (a *API) handleHot(w http.ResponseWriter, r *http.Request) {
	res, err := a.bgm.Hot(r.Context(), intParam(r, "limit", 24, 1, 50), intParam(r, "offset", 0, 0, 10000))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "获取热门失败: "+err.Error())
		return
	}
	fixCards(res.Items)
	writeJSON(w, res)
}

// GET /api/anime/calendar 每周放送日历
func (a *API) handleCalendar(w http.ResponseWriter, r *http.Request) {
	days, err := a.bgm.Calendar(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "获取时间表失败: "+err.Error())
		return
	}
	for i := range days {
		fixCards(days[i].Items)
	}
	writeJSON(w, days)
}

// GET /api/anime/search?q=&offset=&limit=
func (a *API) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, bangumi.SearchResult{Items: []bangumi.Card{}})
		return
	}
	res, err := a.bgm.Search(r.Context(), q, intParam(r, "limit", 24, 1, 50), intParam(r, "offset", 0, 0, 10000))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "搜索失败: "+err.Error())
		return
	}
	fixCards(res.Items)
	writeJSON(w, res)
}

func subjectID(r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	return id, err == nil && id > 0
}

// GET /api/anime/subject/{id}
func (a *API) handleSubject(w http.ResponseWriter, r *http.Request) {
	id, ok := subjectID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "番剧 id 不对")
		return
	}
	s, err := a.bgm.Subject(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "获取番剧详情失败: "+err.Error())
		return
	}
	s.Images = proxyImages(s.Images)
	writeJSON(w, s)
}

// GET /api/anime/subject/{id}/episodes
func (a *API) handleEpisodes(w http.ResponseWriter, r *http.Request) {
	id, ok := subjectID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "番剧 id 不对")
		return
	}
	eps, err := a.bgm.Episodes(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "获取剧集失败: "+err.Error())
		return
	}
	if eps == nil {
		eps = []bangumi.Episode{}
	}
	writeJSON(w, eps)
}
