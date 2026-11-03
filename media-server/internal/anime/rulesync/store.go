package rulesync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"media-server/internal/anime/rules"
)

// writeDisk 把换完之后的整套规则写进 Dir。规则目录由同步器托管：
//   - rules.Load 是"磁盘有规则就只用磁盘、否则只用内嵌"，所以第一次同步（启动时用的还是内嵌那份）
//     要把整套都写出去，不能只写变了的几条，不然重启后其余的就没了；
//   - 文件名一律用规则自己的名字，不在这一整套里的 .json 扫掉（上游下架的站就是这么删掉的）；
//   - 换新版的站，先把正在用的那份存进 previous/（每站一份），存不进去整轮都不换；
//     previous/ 里下架站的那份也一起扫掉，不能借上一版让它回来。
func (s *Syncer) writeDisk(list []*rules.Rule, old map[string]*rules.Rule) error {
	dir := s.o.Dir
	prevDir := filepath.Join(dir, rules.PreviousDir)
	for _, r := range list { // 先都查一遍，有一条写不了就一个文件都不动
		if !safeName.MatchString(r.Name) {
			return fmt.Errorf("规则名 %q 不能当文件名", r.Name)
		}
		if len(r.Raw()) == 0 { // 不是从 JSON 读进来的规则，写个空文件重启后这条就没了
			return fmt.Errorf("规则 %s 没有原文", r.Name)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// 第一步：要换新版的站，先把正在用的那份存成上一版（新的顶掉更早的，每站只留一份）
	for _, r := range list {
		o := old[strings.ToLower(r.Name)]
		if o == nil || o == r { // 新加的站没有上一版；没换的站不动
			continue
		}
		if len(o.Raw()) == 0 {
			return fmt.Errorf("规则 %s 正在用的那份没有原文，存不了上一版", o.Name)
		}
		if o.Name != r.Name {
			if err := os.Remove(filepath.Join(prevDir, o.Name+".json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := writeFile(filepath.Join(prevDir, r.Name+".json"), o.Raw()); err != nil {
			return fmt.Errorf("保存 %s 的上一版失败：%w", r.Name, err)
		}
	}
	keep := make(map[string]bool, len(list))
	type item struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	index := make([]item, 0, len(list))
	for _, r := range list {
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
	// 下架站的上一版：当前文件已经没了，Load 本来就不会去读它，这里扫掉是不留尾巴
	if prev, err := os.ReadDir(prevDir); err == nil {
		for _, e := range prev {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".json") || keep[strings.ToLower(name)] {
				continue
			}
			if err := os.Remove(filepath.Join(prevDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				log.Printf("番剧规则：删除下架站的上一版 %s 失败: %v", name, err)
			}
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
