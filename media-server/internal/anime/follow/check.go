package follow

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"media-server/internal/anime/engine"
)

// checkResult 查一次的结果
type checkResult struct {
	discovered bool // 这次是盒子自己找的片源
	rule, src  string
	srcName    string
	count      int
	latest     string
	err        string
}

// check 去片源站看现在有几集；还没有片源就先找一个
func (s *Service) check(ctx context.Context, f Follow) checkResult {
	if f.Rule == "" {
		return s.discover(ctx, f)
	}
	r := checkResult{rule: f.Rule, src: f.Src}
	roads, err := s.d.Chapters(ctx, f.Rule, f.Src)
	if err != nil {
		r.err = err.Error()
		return r
	}
	if r.count, r.latest = countOf(roads); r.count == 0 {
		r.err = "片源站上这部番一集都没有"
	}
	return r
}

// discover 没指定片源时盒子自己挑：各站搜一遍，名字对得上的里面挑集数最多的（最新的）
func (s *Service) discover(ctx context.Context, f Follow) checkResult {
	titles := searchTitles(f.Title, f.Name)
	type cand struct {
		order, score int
		rule         string
		item         engine.Item
	}
	var (
		mu    sync.Mutex
		cands []cand
		wg    sync.WaitGroup
		sem   = make(chan struct{}, 3) // 最多同时搜 3 个站
	)
	for i, rule := range s.d.Rules() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, t := range titles {
				sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				items, err := s.d.Search(sctx, rule, t)
				cancel()
				if err != nil || len(items) == 0 {
					continue
				}
				if it, sc := bestItem(items, titles); sc > 0 {
					mu.Lock()
					cands = append(cands, cand{order: i, score: sc, rule: rule, item: it})
					mu.Unlock()
				}
				return
			}
		}()
	}
	wg.Wait()
	sort.Slice(cands, func(a, b int) bool {
		if cands[a].score != cands[b].score {
			return cands[a].score > cands[b].score
		}
		return cands[a].order < cands[b].order
	})
	// 只在名字最像的那一档里比集数（不然"第二季"这种只是包含关系的会因为集数多被选上）
	best := checkResult{err: "片源站上还没找到这部番"}
	for i, c := range cands {
		if i >= 4 || c.score < cands[0].score {
			break
		}
		roads, err := s.d.Chapters(ctx, c.rule, c.item.Src)
		if err != nil {
			continue
		}
		if n, latest := countOf(roads); n > best.count {
			best = checkResult{discovered: true, rule: c.rule, src: c.item.Src, srcName: c.rule, count: n, latest: latest}
		}
	}
	return best
}

// schedule 下次什么时候查：刚播完那几个小时勤快点，等到了就歇到下周
func (s *Service) schedule(f Follow, now time.Time) time.Time {
	e, ok := s.d.Air(f.ID)
	if !ok || e.JP.Period <= 0 {
		return now.Add(3 * time.Hour) // 不知道几点播，隔几个小时看一次
	}
	upTo := func(t time.Time, d time.Duration) time.Time {
		if t.IsZero() || t.After(now.Add(d)) {
			return now.Add(d)
		}
		return t
	}
	next, last := e.JP.Next(now, e.End), e.JP.Last(now)
	if last.IsZero() || f.UpdatedAt.After(last) { // 还没开播 / 这周的已经等到了
		return upTo(next, 24*time.Hour)
	}
	since := now.Sub(last)
	switch {
	case since < 6*time.Hour: // 片源站一般播出后一两个小时上新
		return now.Add(20 * time.Minute)
	case since < 30*time.Hour:
		return now.Add(time.Hour)
	case next.IsZero() && since > 7*24*time.Hour: // 早就播完了
		return now.Add(24 * time.Hour)
	default:
		return upTo(next, 6*time.Hour)
	}
}

// countOf 各线路里集数最多的那条：有几集、最后一集叫什么
func countOf(roads []engine.Road) (int, string) {
	n, latest := 0, ""
	for _, r := range roads {
		if len(r.Episodes) > n {
			n = len(r.Episodes)
			latest = epLabel(r.Episodes[n-1].Name, n)
		}
	}
	return n, latest
}

var reNum = regexp.MustCompile(`^(?:第\s*)?(?:(?i:ep?)\s*)?0*(\d{1,4})\s*(?:集|话|話)?$`)

// epLabel 集名统一成"第5集"（认不出数字的原样留着，比如"特别篇"）
func epLabel(name string, n int) string {
	name = strings.TrimSpace(name)
	if m := reNum.FindStringSubmatch(name); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil && v > 0 {
			return "第" + strconv.Itoa(v) + "集"
		}
	}
	if name == "" {
		return "第" + strconv.Itoa(n) + "集"
	}
	return name
}

// ---- 标题比对（和前端 useSources.ts 同一套规则） ----

func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var (
	reSubtitle = regexp.MustCompile(`[～~].*?[～~]`)
	reSeason   = regexp.MustCompile(`第[一二三四五六七八九十0-9]+[季期部]`)
	reSpaces   = regexp.MustCompile(`\s+`)
)

// simplifyTitle 去掉片源站通常不带的"～副标题～""第N季"
func simplifyTitle(s string) string {
	s = reSeason.ReplaceAllString(reSubtitle.ReplaceAllString(s, " "), " ")
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

func searchTitles(nameCN, name string) []string {
	var out []string
	for _, t := range []string{nameCN, simplifyTitle(nameCN), name, simplifyTitle(name)} {
		if t != "" && !contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

var (
	reSeasonCN  = regexp.MustCompile(`第\s*([一二三四五六七八九十0-9]+)\s*[季期部]`)
	reSeasonEN  = regexp.MustCompile(`(?i)(?:(\d+)(?:st|nd|rd|th)\s*season|season\s*(\d+)|\bs(\d+)\b)`)
	reSeasonEnd = regexp.MustCompile(`(?:[^0-9a-zA-Z]|^)([2-9])\s*$`) // "转生成为魔剑2"（只认结尾单个数字，免得把年份当季数）
	romanNums   = map[string]int{"Ⅱ": 2, "Ⅲ": 3, "Ⅳ": 4, "Ⅴ": 5, "Ⅵ": 6}
	reRomanEnd  = regexp.MustCompile(`(?:[^A-Za-z]|^)(II|III|IV)\s*$`) // "転生したら剣でしたII"、"Overlord III"
	cnNums      = map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9, '十': 10}
)

// seasonOf 从名字里认第几季，认不出算第 1 季
func seasonOf(s string) int {
	s = strings.TrimSpace(s)
	if m := reSeasonCN.FindStringSubmatch(s); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
		n := 0
		for _, r := range m[1] { // 只处理到"十几"
			if r == '十' {
				n = max(n, 1) * 10
			} else {
				n += cnNums[r]
			}
		}
		if n > 0 {
			return n
		}
	}
	if m := reSeasonEN.FindStringSubmatch(s); m != nil {
		for _, g := range m[1:] {
			if n, err := strconv.Atoi(g); err == nil && n > 0 {
				return n
			}
		}
	}
	for k, v := range romanNums {
		if strings.HasSuffix(s, k) || strings.Contains(s, k+" ") {
			return v
		}
	}
	if m := reRomanEnd.FindStringSubmatch(s); m != nil {
		return map[string]int{"II": 2, "III": 3, "IV": 4}[m[1]]
	}
	if m := reSeasonEnd.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 1
}

// bestItem 名字完全相同 2 分，互相包含 1 分，同分取靠前的；
// 季数对不上的不要（去掉"第二季"再比时，第一季的名字会和它一模一样）
func bestItem(items []engine.Item, titles []string) (engine.Item, int) {
	var ts []string
	season := 1
	for _, t := range titles {
		if n := norm(t); n != "" {
			ts = append(ts, n)
		}
		season = max(season, seasonOf(t))
	}
	best, bestScore := engine.Item{}, 0
	for _, it := range items {
		if seasonOf(it.Name) != season {
			continue
		}
		n, sc := norm(it.Name), 0
		for _, t := range ts {
			if n == t {
				sc = 2
				break
			}
			if n != "" && (strings.Contains(n, t) || strings.Contains(t, n)) {
				sc = 1
			}
		}
		if sc > bestScore {
			best, bestScore = it, sc
		}
	}
	return best, bestScore
}
