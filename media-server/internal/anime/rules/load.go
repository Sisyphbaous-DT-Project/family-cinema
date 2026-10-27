package rules

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Set 已加载的规则集合（只读，加载后不再变动）
type Set struct {
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
	s := &Set{byName: map[string]*Rule{}}
	if fsys == nil {
		return s
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		log.Printf("anime: 读取规则目录失败: %v", err)
		return s
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == "index.json" {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			continue
		}
		r, err := ParseRule(data)
		if err != nil {
			log.Printf("anime: 规则 %s 解析失败: %v", filepath.Base(name), err)
			continue
		}
		key := strings.ToLower(r.Name)
		if _, dup := s.byName[key]; dup {
			continue
		}
		s.byName[key] = r
		s.list = append(s.list, r)
	}
	sort.Slice(s.list, func(i, j int) bool {
		return strings.ToLower(s.list[i].Name) < strings.ToLower(s.list[j].Name)
	})
	return s
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

// All 按名称排序的全部规则
func (s *Set) All() []*Rule { return s.list }

// Get 按名称（不区分大小写）取规则
func (s *Set) Get(name string) *Rule { return s.byName[strings.ToLower(name)] }

// Infos 规则摘要列表（给前端）
func (s *Set) Infos() []Info {
	out := make([]Info, 0, len(s.list))
	for _, r := range s.list {
		out = append(out, r.Info())
	}
	return out
}
