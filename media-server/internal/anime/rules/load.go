package rules

import (
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// PreviousDir 规则目录下存"每站上一份通过检查的规则"的子目录（rulesync 换新版前写进去）。
// 只在当前文件坏掉时拿来救急，Load 不会把它当成当前规则加载。
const PreviousDir = "previous"

// Set 已加载的规则集合。每日同步（rulesync）会用 Replace 整份换上新规则，所以读写都加锁；
// 换的是一份新列表，旧列表和旧的 *Rule 都不会被改动，正在用旧规则的请求照常跑完。
type Set struct {
	mu     sync.RWMutex
	list   []*Rule
	byName map[string]*Rule
}

// Load 加载规则：磁盘目录 dir 里有 *.json 就用磁盘的（改规则只推文件、不用重编二进制），
// 否则回退到编译进二进制的 embedded（与 web 前端文件同一套"磁盘优先、embed 兜底"思路）。
func Load(dir string, embedded fs.FS) *Set {
	var fsys fs.FS
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() && hasJSON(os.DirFS(dir)) {
			fsys = os.DirFS(dir)
		}
	}
	if fsys == nil && embedded != nil {
		fsys = embedded
	}
	if fsys == nil {
		return NewSet(nil)
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		log.Printf("anime: 读取规则目录失败: %v", err)
		return NewSet(nil)
	}
	var list []*Rule
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == "index.json" {
			continue
		}
		var r *Rule
		data, err := fs.ReadFile(fsys, name)
		if err == nil {
			r, err = ParseRule(data)
		}
		if err != nil {
			// 当前文件读不出来或坏了（比如写到一半断电）：有同站的上一版就先用上一版
			if p := loadPrevious(fsys, name); p != nil {
				log.Printf("anime: 规则 %s 读不出来（%v），改用上一版 %s", filepath.Base(name), err, p.Version)
				list = append(list, p)
				continue
			}
			log.Printf("anime: 规则 %s 解析失败: %v", filepath.Base(name), err)
			continue
		}
		list = append(list, r)
	}
	return NewSet(list)
}

// loadPrevious 读 previous/ 里同一个文件名的上一版；名字得对得上，免得拿别的站顶替
func loadPrevious(fsys fs.FS, name string) *Rule {
	data, err := fs.ReadFile(fsys, path.Join(PreviousDir, name))
	if err != nil {
		return nil
	}
	r, err := ParseRule(data)
	if err != nil || !strings.EqualFold(r.Name, strings.TrimSuffix(name, ".json")) {
		return nil
	}
	return r
}

func hasJSON(fsys fs.FS) bool {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && e.Name() != "index.json" {
			return true
		}
	}
	return false
}

// NewSet 用一组规则建集合（同名不区分大小写，只留先出现的那条）
func NewSet(list []*Rule) *Set {
	s := &Set{}
	s.Replace(list)
	return s
}

// Replace 整份换上新规则；排序、去重规则与 Load 一致
func (s *Set) Replace(list []*Rule) {
	byName := make(map[string]*Rule, len(list))
	out := make([]*Rule, 0, len(list))
	for _, r := range list {
		key := strings.ToLower(r.Name)
		if _, dup := byName[key]; dup {
			continue
		}
		byName[key] = r
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	s.mu.Lock()
	s.list, s.byName = out, byName
	s.mu.Unlock()
}

// All 按名称排序的全部规则（返回的切片不会再被改动，可以放心遍历）
func (s *Set) All() []*Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.list
}

// Get 按名称（不区分大小写）取规则
func (s *Set) Get(name string) *Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byName[strings.ToLower(name)]
}

// Infos 规则摘要列表（给前端）
func (s *Set) Infos() []Info {
	list := s.All()
	out := make([]Info, 0, len(list))
	for _, r := range list {
		out = append(out, r.Info())
	}
	return out
}
