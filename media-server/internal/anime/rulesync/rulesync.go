// Package rulesync 每天去社区规则仓库（KazumiRules）看一眼：有新版就换、新增的站加上、
// 上游删掉的站也删掉，换完不用重启（rules.Set.Replace 整份换）。
//
// 盒子直连 GitHub 被墙，用两个国内能连上的镜像（2026-11-02 盒子实测）：
// gh-proxy 内容最新；GitCode 快，但会落后好几天。所以两边都问，每条规则取版本高的那份，
// 并且永不降级——跟 Kazumi 的做法一致（它的注释原话意思：镜像可能落后）。
//
// 新版换上之前先在本地验收（不联网）：表达式引擎认不认得、要不要验证码、模板能不能展开。
// 不合格的按站跳过、继续用当前那份，不影响别的站更新；合格的换上前把当前那份存进 previous/，
// 当前文件哪天坏了，启动时还能拿上一版顶上（见 rules.Load）。
package rulesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"media-server/internal/anime/engine"
	"media-server/internal/anime/rules"
)

// Source 一个规则仓库地址：清单是 Base+"index.json"，规则文件是 Base+名字+".json"
type Source struct {
	Name string
	Base string
}

// DefaultSources 默认的两个镜像
var DefaultSources = []Source{
	{Name: "gh-proxy", Base: "https://gh-proxy.org/https://raw.githubusercontent.com/Predidit/KazumiRules/main/"},
	{Name: "GitCode", Base: "https://raw.gitcode.com/gh_mirrors/ka/KazumiRules/raw/main/"},
}

const (
	// maxAPILevel 与 Kazumi 当前的 apiLevel 一致：规则要求更高，说明用了引擎还不认识的写法，先不换
	maxAPILevel  = 9
	maxIndexSize = 1 << 20
	maxRuleSize  = 512 << 10
	retryAfter   = time.Hour // 这一轮没成功，一小时后再试
	firstDelay   = 2 * time.Minute
)

// beijing 每天凌晨四点多（北京时间）查一次；盒子上没有时区库，用固定时区
var beijing = time.FixedZone("北京时间", 8*3600)

// Options 同步要用的东西
type Options struct {
	HTTP      *http.Client
	Rules     *rules.Set
	Dir       string // 规则落盘目录，和启动时 rules.Load 读的是同一个
	StateFile string // 同步状态存哪（给设置页看）
	Sources   []Source
	Now       func() time.Time
	// Validate 新版换上之前的本地检查（不联网），为空用 engine.ValidateRule；
	// 验证码、缺字段这些在它之前已经用 Rule.Unsupported 查过了
	Validate func(*rules.Rule) error
}

// Change 一条规则的变化
type Change struct {
	Name string `json:"name"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// Skip 上游有新版但这次没换的规则
type Skip struct {
	Name    string `json:"name"`
	Version string `json:"version"` // 上游的版本
	Reason  string `json:"reason"`
	// Kind 给设置页挑说法：captcha 要验证码、invalid 引擎暂未兼容、engine 要更新盒子程序、
	// download 没下载下来（一小时后再试）、mismatch 镜像给的文件不对。旧版本存的状态里没有这个字段
	Kind string `json:"kind,omitempty"`
	// CurrentVersion 还在继续用的那一版；新站没有旧版、这次也没启用时为空
	CurrentVersion string `json:"currentVersion,omitempty"`
}

// 候选规则没换成的几类原因（Skip.Kind）
const (
	kindDownload = "download"
	kindMismatch = "mismatch"
	kindEngine   = "engine"
	kindCaptcha  = "captcha"
	kindInvalid  = "invalid"
)

// skipErr 一个候选为什么没用上。sure 为真表示文件确实就是清单写的那个版本、只是引擎用不了它：
// 同一个版本等多久都一样，不用一小时后重试，也不去落后的镜像找更旧的版本顶替
type skipErr struct {
	kind, reason string
	detail       string // 具体原因，只记日志
	sure         bool
}

func (e *skipErr) Error() string { return e.reason }

// rank 几个候选都没成时，挑最能说明问题的原因给人看
func (e *skipErr) rank() int {
	switch {
	case e.sure:
		return 2
	case e.kind == kindMismatch:
		return 1
	}
	return 0
}

// SourceStatus 一个镜像这一轮的情况
type SourceStatus struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Count int    `json:"count,omitempty"`
	Error string `json:"error,omitempty"`
}

// Status 给设置页看的同步情况
type Status struct {
	Enabled   bool           `json:"enabled"`
	Running   bool           `json:"running"`
	LastCheck time.Time      `json:"lastCheck,omitzero"`
	LastOK    bool           `json:"lastOk"`
	Error     string         `json:"error,omitempty"`
	Note      string         `json:"note,omitempty"`
	Sources   []SourceStatus `json:"sources,omitempty"`
	Skipped   []Skip         `json:"skipped,omitempty"`
	// 最近一次真有变化的那一轮
	LastChanged time.Time `json:"lastChanged,omitzero"`
	Updated     []Change  `json:"updated,omitempty"`
	Added       []Change  `json:"added,omitempty"`
	Removed     []Change  `json:"removed,omitempty"`
}

// Syncer 规则同步器
type Syncer struct {
	o     Options
	runMu sync.Mutex // 同一时间只跑一轮：定时的和设置页手动点的都走 Check
	mu    sync.Mutex // 管 st
	st    Status
}

// New 读出上次的同步状态
func New(o Options) *Syncer {
	if o.Now == nil {
		o.Now = time.Now
	}
	if len(o.Sources) == 0 {
		o.Sources = DefaultSources
	}
	if o.Validate == nil {
		o.Validate = engine.ValidateRule
	}
	s := &Syncer{o: o}
	if b, err := os.ReadFile(o.StateFile); err == nil {
		_ = json.Unmarshal(b, &s.st)
	}
	s.st.Running = false
	return s
}

// Status 当前情况（切片只会整个换掉、不会原地改，直接给出去没事）
func (s *Syncer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	st.Enabled = true
	return st
}

// Run 开机两分钟后查一次，之后每天凌晨四点多查一次；没查成功就一小时后再试
func (s *Syncer) Run(ctx context.Context) {
	minute := rand.IntN(60) // 别和别人家的盒子挤在同一分钟
	wait := firstDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if _, again := s.check(ctx); again {
			wait = retryAfter
		} else {
			wait = untilDaily(s.o.Now(), minute)
		}
	}
}

// untilDaily 距离下一个北京时间 4:minute 还有多久
func untilDaily(now time.Time, minute int) time.Duration {
	n := now.In(beijing)
	next := time.Date(n.Year(), n.Month(), n.Day(), 4, minute, 0, 0, beijing)
	if !next.After(n) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(n)
}

// Check 马上查一轮（已经有一轮在跑就等它跑完再查），返回查完后的情况
func (s *Syncer) Check(ctx context.Context) Status {
	st, _ := s.check(ctx)
	return st
}

// check 同 Check，另外告诉定时器要不要一小时后再查：整轮没成功、有仓库没连上、
// 或者有该换的规则因为网络没下载下来，等到明天就太久了
func (s *Syncer) check(ctx context.Context) (Status, bool) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.mu.Lock()
	s.st.Running = true
	s.mu.Unlock()
	res := s.runOnce(ctx)
	s.finish(res)
	return s.Status(), res.err != "" || res.retry
}

type result struct {
	sources                 []SourceStatus
	err, note               string
	skipped                 []Skip
	updated, added, removed []Change
	changed                 bool
	retry                   bool // 有仓库没连上、或有规则因为网络没下载下来：一小时后再查
}

// entry 清单里的一条
type entry struct {
	name, version string
}

// cand 某条规则在某个镜像上的版本
type cand struct {
	src     Source
	name    string
	version string
}

func (s *Syncer) runOnce(ctx context.Context) (res result) {
	indexes := make([]map[string]entry, len(s.o.Sources)) // 每个镜像：小写名 → 清单条目
	allOK, anyOK := true, false
	for i, src := range s.o.Sources {
		st := SourceStatus{Name: src.Name}
		m, err := s.fetchIndex(ctx, src)
		if err != nil {
			st.Error, allOK = err.Error(), false
		} else {
			st.OK, st.Count, anyOK = true, len(m), true
			indexes[i] = m
		}
		res.sources = append(res.sources, st)
	}
	if !anyOK {
		res.err = "规则仓库都没连上"
		return res
	}
	res.retry = !allOK

	current := s.o.Rules.All()
	byKey := make(map[string]*rules.Rule, len(current))
	for _, r := range current {
		byKey[strings.ToLower(r.Name)] = r
	}
	next := make(map[string]*rules.Rule, len(current)+4) // 换完之后的整套
	for k, r := range byKey {
		next[k] = r
	}

	// 所有镜像清单里出现过的名字
	listed := map[string]bool{}
	for _, m := range indexes {
		for k := range m {
			listed[k] = true
		}
	}
	keys := make([]string, 0, len(listed))
	for k := range listed {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		var cands []cand
		for i, m := range indexes {
			if e, ok := m[key]; ok {
				if _, ok := parseVersion(e.version); ok {
					cands = append(cands, cand{src: s.o.Sources[i], name: e.name, version: e.version})
				}
			}
		}
		// 版本高的在前，同版本按镜像顺序（gh-proxy 在前）
		sort.SliceStable(cands, func(i, j int) bool {
			c, _ := compareVersion(cands[i].version, cands[j].version)
			return c > 0
		})
		local := byKey[key]
		var got *rules.Rule
		var why *skipErr
		var best, sureVer string // sureVer：确定引擎用不了的那个版本
		var dlFail []string      // 没下载下来的候选版本
		for _, c := range cands {
			if local != nil && !newer(c.version, string(local.Version)) {
				break // 后面的只会更旧
			}
			if cmp, ok := compareVersion(c.version, sureVer); ok && cmp < 0 {
				break // 新版验收没过：不去落后的镜像找更旧的版本顶替，继续用当前那份
			}
			if best == "" {
				best = c.version
			}
			r, err := s.fetchRule(ctx, c)
			if err == nil {
				got = r
				break
			}
			se, _ := err.(*skipErr)
			if se == nil {
				se = &skipErr{kind: kindMismatch, reason: err.Error()}
			}
			// 设置页说的是"上游最新的那个版本为什么没换成"：只在最新版本的几个候选里挑最说明问题的原因
			if cmp, _ := compareVersion(c.version, best); why == nil || (cmp == 0 && se.rank() > why.rank()) {
				why = se
			}
			switch {
			case se.kind == kindDownload:
				dlFail = append(dlFail, c.version)
			case se.sure && sureVer == "":
				sureVer = c.version
			}
		}
		if got == nil {
			if best != "" {
				sk := Skip{Name: cands[0].name, Version: best, Reason: why.reason, Kind: why.kind}
				keep := "这个站先不启用"
				if local != nil {
					sk.CurrentVersion = string(local.Version)
					keep = "继续使用 " + sk.CurrentVersion
				}
				res.skipped = append(res.skipped, sk)
				detail := ""
				if why.detail != "" {
					detail = "（" + why.detail + "）"
				}
				log.Printf("番剧规则 %s 上游 %s 没换成：%s%s，%s", sk.Name, best, why.reason, detail, keep)
			}
			// 只有网络原因没下载下来、而且那个版本不是已经确定用不了的，才值得过一会儿再试；
			// 版本对不上、表达式不认识、要验证码这些等多久都一样
			for _, v := range dlFail {
				if cmp, ok := compareVersion(v, sureVer); !ok || cmp > 0 {
					res.retry = true
				}
			}
			continue
		}
		next[key] = got
		if local != nil {
			res.updated = append(res.updated, Change{Name: got.Name, From: string(local.Version), To: string(got.Version)})
		} else {
			res.added = append(res.added, Change{Name: got.Name, To: string(got.Version)})
		}
	}

	// 删站：只有所有镜像都成功给了清单、而且都不再列出它才删（落后的镜像里还没有新站，
	// 只按一个镜像判断会把刚加的站误删，第二天又加回来）
	if allOK {
		var gone []*rules.Rule
		for key, r := range byKey {
			if !listed[key] {
				gone = append(gone, r)
			}
		}
		switch {
		case len(gone) == 0:
		case len(gone)*2 > len(byKey):
			res.note = fmt.Sprintf("清单里一下子少了 %d 个站，看着不对劲，这次先不删", len(gone))
		default:
			sort.Slice(gone, func(i, j int) bool { return strings.ToLower(gone[i].Name) < strings.ToLower(gone[j].Name) })
			for _, r := range gone {
				delete(next, strings.ToLower(r.Name))
				res.removed = append(res.removed, Change{Name: r.Name, From: string(r.Version)})
			}
		}
	} else {
		res.note = "有一个规则仓库没连上，这次只更新、不删站"
	}

	if len(res.updated)+len(res.added)+len(res.removed) == 0 {
		return res
	}
	list := make([]*rules.Rule, 0, len(next))
	for _, r := range next {
		list = append(list, r)
	}
	if err := s.writeDisk(list, byKey); err != nil {
		res.err = "规则写进硬盘失败：" + err.Error()
		res.updated, res.added, res.removed = nil, nil, nil
		return res
	}
	s.o.Rules.Replace(list)
	res.changed = true
	return res
}

// fetchIndex 取一个镜像的清单；拿不到、不是 JSON、一条能用的都没有、一个版本都看不懂，
// 都算这个镜像失败（失败的镜像这轮不参与删站，免得一份坏清单把别的站都删了）
func (s *Syncer) fetchIndex(ctx context.Context, src Source) (map[string]entry, error) {
	data, err := s.get(ctx, src.Base+"index.json", maxIndexSize)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name    string          `json:"name"`
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.New("清单不是 JSON")
	}
	m := map[string]entry{}
	for _, x := range raw {
		if !safeName.MatchString(x.Name) {
			continue
		}
		key := strings.ToLower(x.Name)
		if _, dup := m[key]; !dup {
			m[key] = entry{name: x.Name, version: rawVersion(x.Version)}
		}
	}
	if len(m) == 0 {
		return nil, errors.New("清单是空的")
	}
	for _, e := range m {
		if _, ok := parseVersion(e.version); ok {
			return m, nil
		}
	}
	return nil, errors.New("清单里的版本号一个都看不懂")
}

// rawVersion 清单里的版本可能写成 "1.6" 也可能写成 1.6
func rawVersion(b json.RawMessage) string {
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(b))
}

// fetchRule 下载一条规则并验收：名字对得上、文件里的版本就是清单写的版本、引擎等级够、
// 不要验证码、表达式和模板本地检查通过。没通过返回 *skipErr，当前那份规则原样留着。
func (s *Syncer) fetchRule(ctx context.Context, c cand) (*rules.Rule, error) {
	data, err := s.get(ctx, c.src.Base+c.name+".json", maxRuleSize)
	if err != nil {
		// 网络、镜像临时出错，过一会儿再试可能就好了
		return nil, &skipErr{kind: kindDownload, reason: fmt.Sprintf("%s 下载失败：%v", c.src.Name, err)}
	}
	r, err := rules.ParseRule(data)
	if err != nil {
		return nil, &skipErr{kind: kindMismatch, reason: fmt.Sprintf("%s 给的规则读不懂", c.src.Name), detail: err.Error()}
	}
	if !strings.EqualFold(r.Name, c.name) || !safeName.MatchString(r.Name) {
		return nil, &skipErr{kind: kindMismatch, reason: fmt.Sprintf("%s 给的规则名字对不上（%s）", c.src.Name, r.Name)}
	}
	if cmp, ok := compareVersion(string(r.Version), c.version); !ok || cmp != 0 {
		return nil, &skipErr{kind: kindMismatch, reason: fmt.Sprintf("%s 清单写 %s，文件里却是 %s", c.src.Name, c.version, r.Version)}
	}
	// 往下的都是"文件没问题，就是这个版本本身用不了"
	lv, ok := apiLevel(string(r.API))
	if !ok || lv > maxAPILevel {
		return nil, &skipErr{kind: kindEngine, reason: fmt.Sprintf("规则要求更新的引擎（api %s）", r.API), sure: true}
	}
	switch why := r.Unsupported(); why {
	case "":
	case "captcha":
		return nil, &skipErr{kind: kindCaptcha, reason: "需要验证码", detail: c.src.Name, sure: true}
	default:
		return nil, &skipErr{kind: kindInvalid, reason: "规则内容不完整", detail: why, sure: true}
	}
	if err := s.o.Validate(r); err != nil {
		detail := err.Error()
		var p *engine.RuleProblem
		if errors.As(err, &p) {
			detail = p.Detail()
		}
		return nil, &skipErr{kind: kindInvalid, reason: err.Error(), detail: detail, sure: true}
	}
	return r, nil
}

func (s *Syncer) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "family-cinema-rulesync/1.0")
	resp, err := s.o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("文件太大")
	}
	return data, nil
}

func (s *Syncer) finish(res result) {
	now := s.o.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Running = false
	s.st.LastCheck = now
	s.st.LastOK = res.err == ""
	s.st.Error, s.st.Note = res.err, res.note
	s.st.Sources, s.st.Skipped = res.sources, res.skipped
	if res.changed {
		s.st.LastChanged = now
		s.st.Updated, s.st.Added, s.st.Removed = res.updated, res.added, res.removed
		log.Printf("番剧规则已更新：更新 %s；新增 %s；下架 %s",
			names(res.updated), names(res.added), names(res.removed))
	}
	if res.err != "" {
		log.Printf("番剧规则同步没成功: %s", res.err)
	}
	if b, err := json.MarshalIndent(s.st, "", " "); err == nil {
		_ = writeFile(s.o.StateFile, b)
	}
}

func names(cs []Change) string {
	if len(cs) == 0 {
		return "无"
	}
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return strings.Join(out, "、")
}

// writeFile 先写临时文件再改名，写到一半断电也不会留下半截的 .json
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
