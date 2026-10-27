package engine

// 接口模式（searchMode/chapterMode = "api"）：按规则里的请求模板调站点的 JSON 接口，
// 再用受限 JSONPath 取搜索结果和选集。行为对齐 Kazumi 的 api_rule_strategy.dart（只借鉴做法，没拷代码）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"media-server/internal/anime/rules"
)

// renderTemplate 把模板里的 @变量 换成值。@ 前面紧挨着字母、数字、下划线的不算变量（如邮箱）。
// encode 为真时按 encodeURIComponent 转义（拼在地址里用）。变量不存在算规则写错，报错。
func renderTemplate(tpl string, vars map[string]any, encode bool) (string, error) {
	var b strings.Builder
	for i := 0; i < len(tpl); {
		c := tpl[i]
		if c != '@' || (i > 0 && isIdentChar(tpl[i-1])) || i+1 >= len(tpl) || !isIdentStart(tpl[i+1]) {
			b.WriteByte(c)
			i++
			continue
		}
		j := i + 1
		for j < len(tpl) && isIdentChar(tpl[j]) {
			j++
		}
		name := tpl[i+1 : j]
		v, ok := vars[name]
		if !ok {
			return "", fmt.Errorf("规则缺少模板变量 @%s", name)
		}
		s := anyString(v)
		if encode {
			s = encodeComponent(s)
		}
		b.WriteString(s)
		i = j
	}
	return b.String(), nil
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// isIdent 整个字符串是不是一个合法的变量名
func isIdent(s string) bool {
	if s == "" || !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentChar(s[i]) {
			return false
		}
	}
	return true
}

// renderValue 渲染请求里的值：整个值就是 "@变量" 时原样换成变量（保留类型），
// 字符串里夹着变量时做文本替换，数组、对象逐项渲染。
func renderValue(v any, vars map[string]any) (any, error) {
	switch t := v.(type) {
	case string:
		if len(t) > 1 && t[0] == '@' && isIdent(t[1:]) {
			x, ok := vars[t[1:]]
			if !ok {
				return nil, fmt.Errorf("规则缺少模板变量 %s", t)
			}
			return x, nil
		}
		return renderTemplate(t, vars, false)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			r, err := renderValue(x, vars)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		return renderMap(t, vars)
	default:
		return v, nil
	}
}

func renderMap(m map[string]any, vars map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for k, v := range m {
		rk, err := renderTemplate(k, vars, false)
		if err != nil {
			return nil, err
		}
		rv, err := renderValue(v, vars)
		if err != nil {
			return nil, err
		}
		out[rk] = rv
	}
	return out, nil
}

// anyString 把规则/响应里的值写成字符串（数字不带多余的小数点和指数）
func anyString(v any) string {
	switch t := v.(type) {
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	default:
		return jsonString(v)
	}
}

// encodeComponent 等同 JS 的 encodeURIComponent（Kazumi 用的 Uri.encodeComponent）
func encodeComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// apiCall 按请求模板调一次接口，返回解码后的 JSON（数字是 json.Number）
func (e *Engine) apiCall(ctx context.Context, r *rules.Rule, cfg rules.APIRequest, vars map[string]any) (any, error) {
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		return nil, fmt.Errorf("接口只支持 GET/POST，规则写的是 %s", method)
	}
	raw, err := renderTemplate(strings.TrimSpace(cfg.URL), vars, true)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("接口地址不对: %s", raw)
	}
	if len(cfg.Query) > 0 {
		q, err := renderMap(cfg.Query, vars)
		if err != nil {
			return nil, err
		}
		qs := u.Query()
		for k, v := range q {
			qs.Set(k, anyString(v))
		}
		u.RawQuery = qs.Encode()
	}
	var body io.Reader
	contentType := ""
	if method == http.MethodPost && cfg.BodyType != "" && cfg.BodyType != "none" {
		rb, err := renderValue(cfg.Body, vars)
		if err != nil {
			return nil, err
		}
		switch cfg.BodyType {
		case "json":
			data, err := json.Marshal(rb)
			if err != nil {
				return nil, err
			}
			body, contentType = bytes.NewReader(data), "application/json"
		case "form":
			form := url.Values{}
			if m, ok := rb.(map[string]any); ok {
				for k, v := range m {
					form.Set(k, anyString(v))
				}
			}
			body, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	ua := r.UserAgent
	if ua == "" {
		ua = DefaultUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if ref := firstNonEmpty(r.Referer, r.BaseURL); ref != "" {
		req.Header.Set("Referer", ref)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// 规则自带的请求头最后设，可以覆盖上面的默认值（如 apikey、Authorization、User-Agent）
	if len(cfg.Headers) > 0 {
		h, err := renderMap(cfg.Headers, vars)
		if err != nil {
			return nil, err
		}
		for k, v := range h {
			req.Header.Set(k, anyString(v))
		}
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("接口返回的不是 JSON")
	}
	return doc, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// searchAPI 接口搜索
func (e *Engine) searchAPI(ctx context.Context, r *rules.Rule, kw string) ([]Item, error) {
	cfg := r.SearchAPI
	doc, err := e.apiCall(ctx, r, cfg.Request, map[string]any{"keyword": kw})
	if err != nil {
		return nil, err
	}
	return parseSearchAPI(cfg, doc)
}

func parseSearchAPI(cfg rules.APISearch, doc any) ([]Item, error) {
	nodes, err := jsonRead(doc, cfg.ListPath)
	if err != nil {
		return nil, err
	}
	tpl := strings.TrimSpace(cfg.SourceTemplate)
	var items []Item
	seen := map[string]bool{}
	for _, n := range nodes {
		nv, err := jsonFirst(n, cfg.NamePath)
		if err != nil {
			return nil, err
		}
		sv, err := jsonFirst(n, cfg.SourcePath)
		if err != nil {
			return nil, err
		}
		name, src := jsonString(nv), jsonString(sv)
		if name == "" || src == "" {
			continue
		}
		// 有模板就拼成详情页地址；没有就把来源（通常是编号）原样交给选集那一步
		if tpl != "" {
			src = strings.ReplaceAll(tpl, "@source", encodeComponent(src))
		}
		if seen[src] {
			continue
		}
		seen[src] = true
		items = append(items, Item{Name: cleanText(name), Src: src})
	}
	return items, nil
}

// chaptersAPI 接口选集
func (e *Engine) chaptersAPI(ctx context.Context, r *rules.Rule, src string) ([]Road, error) {
	cfg := r.ChapterAPI
	doc, err := e.apiCall(ctx, r, cfg.Request, map[string]any{"source": src})
	if err != nil {
		return nil, err
	}
	return parseChaptersAPI(r.BaseURL, cfg, doc, src)
}

func parseChaptersAPI(baseURL string, cfg rules.APIChapter, doc any, src string) ([]Road, error) {
	root := map[string]any{"source": src}
	for k, p := range cfg.Variables {
		v, err := jsonFirst(doc, p)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nil, fmt.Errorf("选集接口里没找到变量 %s（%s）", k, p)
		}
		root[k] = v
	}
	if cfg.Format == rules.FormatDelimited {
		return parseDelimited(baseURL, cfg, doc, root)
	}
	return parseNested(baseURL, cfg, doc, root)
}

func parseNested(baseURL string, cfg rules.APIChapter, doc any, root map[string]any) ([]Road, error) {
	hasRoads := strings.TrimSpace(cfg.RoadsPath) != ""
	roadNodes := []any{doc}
	if hasRoads {
		var err error
		if roadNodes, err = jsonRead(doc, cfg.RoadsPath); err != nil {
			return nil, err
		}
	}
	var roads []Road
	for ri, rn := range roadNodes {
		roadName := ""
		if hasRoads && strings.TrimSpace(cfg.RoadNamePath) != "" {
			v, err := jsonFirst(rn, cfg.RoadNamePath)
			if err != nil {
				return nil, err
			}
			roadName = jsonString(v)
		}
		epNodes, err := jsonRead(rn, cfg.EpisodesPath)
		if err != nil {
			return nil, err
		}
		var eps []Episode
		for ei, en := range epNodes {
			nv, err := jsonFirst(en, cfg.EpisodeNamePath)
			if err != nil {
				return nil, err
			}
			raw := ""
			if strings.TrimSpace(cfg.EpisodeURLPath) != "" {
				v, err := jsonFirst(en, cfg.EpisodeURLPath)
				if err != nil {
					return nil, err
				}
				raw = jsonString(v)
			}
			u, err := episodeURL(baseURL, cfg, root, raw, ri, ei)
			if err != nil {
				return nil, err
			}
			if u == "" {
				continue
			}
			eps = append(eps, Episode{Name: firstNonEmpty(jsonString(nv), fmt.Sprintf("第%d集", ei+1)), URL: u})
		}
		if len(eps) == 0 {
			continue
		}
		roads = append(roads, Road{Name: firstNonEmpty(roadName, fmt.Sprintf("线路 %d", len(roads)+1)), Episodes: eps})
	}
	return roads, nil
}

func parseDelimited(baseURL string, cfg rules.APIChapter, doc any, root map[string]any) ([]Road, error) {
	if cfg.RoadSeparator == "" || cfg.EpisodeSeparator == "" || cfg.FieldSeparator == "" {
		return nil, errors.New("规则里的选集分隔符不能为空")
	}
	nv, err := jsonFirst(doc, cfg.RoadNamesPath)
	if err != nil {
		return nil, err
	}
	ev, err := jsonFirst(doc, cfg.RoadEpisodesPath)
	if err != nil {
		return nil, err
	}
	all := jsonString(ev)
	if all == "" {
		return nil, nil
	}
	roadNames := strings.Split(jsonString(nv), cfg.RoadSeparator)
	var roads []Road
	for ri, group := range strings.Split(all, cfg.RoadSeparator) {
		var eps []Episode
		for ei, entry := range strings.Split(group, cfg.EpisodeSeparator) {
			entry = strings.TrimSpace(entry)
			i := strings.Index(entry, cfg.FieldSeparator)
			if entry == "" || i < 0 {
				continue
			}
			name := strings.TrimSpace(entry[:i])
			raw := strings.TrimSpace(entry[i+len(cfg.FieldSeparator):])
			u, err := episodeURL(baseURL, cfg, root, raw, ri, ei)
			if err != nil {
				return nil, err
			}
			if u == "" {
				continue
			}
			eps = append(eps, Episode{Name: firstNonEmpty(name, fmt.Sprintf("第%d集", ei+1)), URL: u})
		}
		if len(eps) == 0 {
			continue
		}
		name := ""
		if ri < len(roadNames) {
			name = strings.TrimSpace(roadNames[ri])
		}
		roads = append(roads, Road{Name: firstNonEmpty(name, fmt.Sprintf("线路 %d", len(roads)+1)), Episodes: eps})
	}
	return roads, nil
}

// episodeURL 算出一集的播放地址：规则给了播放页模板就按模板拼，否则用接口里取到的地址（可能是播放页，也可能直接是视频）
func episodeURL(baseURL string, cfg rules.APIChapter, root map[string]any, raw string, ri, ei int) (string, error) {
	page := cfg.EpisodePage
	if page == nil {
		return normalizeURL(baseURL, raw), nil
	}
	if strings.TrimSpace(page.URL) == "" {
		return "", errors.New("规则里的播放页模板是空的")
	}
	vars := make(map[string]any, len(root)+5)
	for k, v := range root {
		vars[k] = v
	}
	vars["episodeUrl"] = raw
	vars["roadIndex"], vars["roadNumber"] = ri, ri+1
	vars["episodeIndex"], vars["episodeNumber"] = ei, ei+1
	p, err := renderTemplate(page.URL, vars, true)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(p)
	if err != nil {
		return "", fmt.Errorf("播放页地址不对: %s", p)
	}
	if len(page.Query) > 0 {
		q, err := renderMap(page.Query, vars)
		if err != nil {
			return "", err
		}
		qs := u.Query()
		for k, v := range q {
			qs.Set(k, anyString(v))
		}
		u.RawQuery = qs.Encode()
	}
	return normalizeURL(baseURL, u.String()), nil
}

// normalizeURL 把相对地址按 baseURL 补全成绝对地址（对齐 Kazumi 的 normalizeEpisodeUrl）：
// 与 baseURL 同站的统一成 baseURL 的协议；去掉路径末尾多余的 /；空的查询串、片段去掉。
func normalizeURL(baseURL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	ru, err := url.Parse(raw)
	bu, berr := url.Parse(strings.TrimSpace(baseURL))
	validBase := berr == nil && bu.Scheme != "" && bu.Host != ""
	var res *url.URL
	switch {
	case err == nil && ru.Scheme != "" && ru.Host != "":
		res = ru
	case err == nil && validBase:
		res = bu.ResolveReference(ru)
	}
	if res == nil || res.Host == "" {
		return raw
	}
	isHTTP := func(s string) bool { return s == "http" || s == "https" }
	if validBase && isHTTP(bu.Scheme) && isHTTP(res.Scheme) && res.Scheme != bu.Scheme && strings.EqualFold(res.Host, bu.Host) {
		res.Scheme = bu.Scheme
	}
	for len(res.Path) > 1 && strings.HasSuffix(res.Path, "/") {
		res.Path = strings.TrimSuffix(res.Path, "/")
		res.RawPath = strings.TrimSuffix(res.RawPath, "/")
	}
	res.ForceQuery = false
	return res.String()
}
