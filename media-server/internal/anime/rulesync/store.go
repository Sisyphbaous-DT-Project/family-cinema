package rulesync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"media-server/internal/anime/rules"
)

// writeDisk 把换完之后的整套规则写进 Dir。规则目录由同步器托管：
//   - rules.Load 是"磁盘有规则就只用磁盘、否则只用内嵌"，所以第一次同步（启动时用的还是内嵌那份）
//     要把整套都写出去，不能只写变了的几条，不然重启后其余的就没了；
//   - 文件名一律用规则自己的名字，不在这一整套里的 .json 扫掉（上游下架的站就是这么删掉的）。
func (s *Syncer) writeDisk(list []*rules.Rule, old map[string]*rules.Rule) error {
	dir := s.o.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keep := make(map[string]bool, len(list))
	type item struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	index := make([]item, 0, len(list))
	for _, r := range list {
		if !safeName.MatchString(r.Name) {
			return fmt.Errorf("规则名 %q 不能当文件名", r.Name)
		}
		if len(r.Raw()) == 0 { // 不是从 JSON 读进来的规则，写个空文件重启后这条就没了
			return fmt.Errorf("规则 %s 没有原文", r.Name)
		}
		// 上游改了名字的大小写：旧文件先删再写新的（大小写不敏感的盘上先写后删会把新文件删掉）
		if o := old[strings.ToLower(r.Name)]; o != nil && o.Name != r.Name {
			if err := os.Remove(filepath.Join(dir, o.Name+".json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		path := filepath.Join(dir, r.Name+".json")
		if cur, err := os.ReadFile(path); err != nil || !bytes.Equal(cur, r.Raw()) {
			if err := writeFile(path, r.Raw()); err != nil {
				return err
			}
		}
		keep[strings.ToLower(r.Name+".json")] = true
		index = append(index, item{Name: r.Name, Version: string(r.Version)})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == "index.json" || keep[strings.ToLower(name)] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// index.json 只是记一笔盘上实际是哪些规则、什么版本（rules.Load 不读它）
	sort.Slice(index, func(i, j int) bool { return strings.ToLower(index[i].Name) < strings.ToLower(index[j].Name) })
	b, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "index.json"), b)
}
