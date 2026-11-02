package rulesync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"media-server/internal/anime/rules"
)

// ruleJSON 一条能用的 xpath 规则；extra 拼在最后（如 `,"api":"10"`）
func ruleJSON(name, version, extra string) string {
	return fmt.Sprintf(`{"api":"1","name":%q,"version":%q,"searchURL":"https://x.test/s?q=@keyword","searchList":"//a","chapterRoads":"//b"%s}`,
		name, version, extra)
}

func indexJSON(pairs ...string) string {
	var items []string
	for i := 0; i+1 < len(pairs); i += 2 {
		items = append(items, fmt.Sprintf(`{"name":%q,"version":%q}`, pairs[i], pairs[i+1]))
	}
	return "[" + strings.Join(items, ",") + "]"
}

// mirror 假镜像：files 里没有的路径回 404；index 为空串时清单回 500（模拟连不上）
func mirror(t *testing.T, index string, files map[string]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "index.json" {
			if index == "" {
				http.Error(w, "boom", 500)
				return
			}
			fmt.Fprint(w, index)
			return
		}
		body, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustSet(t *testing.T, jsons ...string) *rules.Set {
	var list []*rules.Rule
	for _, j := range jsons {
		r, err := rules.ParseRule([]byte(j))
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, r)
	}
	return rules.NewSet(list)
}

func newSyncer(t *testing.T, set *rules.Set, srvs ...*httptest.Server) (*Syncer, string) {
	dir := filepath.Join(t.TempDir(), "rules")
	var sources []Source
	for i, s := range srvs {
		sources = append(sources, Source{Name: fmt.Sprintf("m%d", i+1), Base: s.URL + "/"})
	}
	return New(Options{
		HTTP:      &http.Client{Timeout: 5 * time.Second},
		Rules:     set,
		Dir:       dir,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		Sources:   sources,
	}), dir
}

func version(set *rules.Set, name string) string {
	if r := set.Get(name); r != nil {
		return string(r.Version)
	}
	return ""
}

// 一个镜像最新、一个落后：取版本高的那份，落后的不能把本地降级；新站加上、两边都不列的站删掉；
// 新版要验证码也照样换上（跟上游走，显示为不可用）；整套写进硬盘，重启后读回来一样
func TestSyncFreshAndStaleMirrors(t *testing.T) {
	set := mustSet(t, ruleJSON("AGE", "1.5", ""), ruleJSON("MXdm", "2.4", ""), ruleJSON("mutefun", "1.4", ""), ruleJSON("old", "1.0", ""))
	fresh := mirror(t, indexJSON("AGE", "1.6", "MXdm", "2.4", "mutefun", "1.6", "TvTFun", "1.3"), map[string]string{
		"AGE.json":     ruleJSON("AGE", "1.6", `,"adBlocker":true`),
		"mutefun.json": ruleJSON("mutefun", "1.6", `,"antiCrawlerConfig":{"enabled":true}`),
		"TvTFun.json":  ruleJSON("TvTFun", "1.3", ""),
	})
	stale := mirror(t, indexJSON("AGE", "1.5", "MXdm", "2.3", "mutefun", "1.4"), map[string]string{
		"MXdm.json": ruleJSON("MXdm", "2.3", ""),
	})
	s, dir := newSyncer(t, set, fresh, stale)
	st := s.Check(context.Background())

	if !st.LastOK || st.Error != "" {
		t.Fatalf("这一轮应该成功: %+v", st)
	}
	if version(set, "AGE") != "1.6" || version(set, "MXdm") != "2.4" || version(set, "TvTFun") != "1.3" || set.Get("old") != nil {
		t.Fatalf("换完的规则不对: %+v", set.Infos())
	}
	if !set.Get("AGE").AdBlocker {
		t.Error("AGE 应该换成新版（打开去广告）")
	}
	if got := set.Get("mutefun").Unsupported(); version(set, "mutefun") != "1.6" || got != "captcha" {
		t.Errorf("mutefun 应该跟上游换成要验证码的新版: v%s %q", version(set, "mutefun"), got)
	}
	if len(st.Updated) != 2 || len(st.Added) != 1 || len(st.Removed) != 1 || st.Removed[0].Name != "old" {
		t.Errorf("变化记录不对: %+v %+v %+v", st.Updated, st.Added, st.Removed)
	}
	if st.LastChanged.IsZero() || !st.LastChanged.Equal(st.LastCheck) {
		t.Error("这一轮有变化，最近变化时间应等于这次检查时间")
	}

	// 盘上是整套（MXdm 没变也要写，因为启动时用的是内嵌那份），下架的站没有文件
	disk := rules.Load(dir, nil)
	if len(disk.All()) != 4 || version(disk, "AGE") != "1.6" || version(disk, "MXdm") != "2.4" || disk.Get("old") != nil {
		t.Fatalf("重启后读回来的规则不对: %+v", disk.Infos())
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err != nil {
		t.Error("应该写一份 index.json")
	}

	// 状态存盘：重新创建（相当于服务重启）后还能看到上次检查
	s2 := New(s.o)
	if got := s2.Status(); !got.LastCheck.Equal(st.LastCheck) || len(got.Added) != 1 || got.Running {
		t.Errorf("状态没存住: %+v", got)
	}

	// 再查一轮：没有变化，最近变化还是上一轮的
	st2 := s.Check(context.Background())
	if !st2.LastChanged.Equal(st.LastChanged) || st2.LastCheck.Before(st.LastCheck) {
		t.Errorf("没变化的一轮不该改最近变化: %+v", st2)
	}
}

// 最新的那个镜像挂了，只剩落后的：落后的清单里还没有新站，也不能因此删掉它；不降级
func TestSyncNoDeleteWhenAMirrorFails(t *testing.T) {
	set := mustSet(t, ruleJSON("AGE", "1.6", ""), ruleJSON("TvTFun", "1.3", ""))
	fresh := mirror(t, "", nil)
	stale := mirror(t, indexJSON("AGE", "1.5"), map[string]string{"AGE.json": ruleJSON("AGE", "1.5", "")})
	s, dir := newSyncer(t, set, fresh, stale)
	st := s.Check(context.Background())

	if !st.LastOK || st.Note == "" {
		t.Fatalf("有一个镜像能用就算成功，并说明这次不删站: %+v", st)
	}
	if set.Get("TvTFun") == nil || version(set, "AGE") != "1.6" {
		t.Fatalf("不该删站也不该降级: %+v", set.Infos())
	}
	if len(st.Sources) != 2 || st.Sources[0].OK || !st.Sources[1].OK {
		t.Errorf("镜像状态不对: %+v", st.Sources)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("没有变化不该动硬盘")
	}
}

func TestSyncAllMirrorsDown(t *testing.T) {
	set := mustSet(t, ruleJSON("AGE", "1.5", ""))
	s, _ := newSyncer(t, set, mirror(t, "", nil), mirror(t, "not json", nil))
	st := s.Check(context.Background())
	if st.LastOK || st.Error == "" || set.Get("AGE") == nil {
		t.Fatalf("都连不上应该报错、规则不动: %+v", st)
	}
}

// 下载回来的文件不对就换另一个镜像，都不对就保留旧版并记下原因
func TestSyncRejectsBadRuleFiles(t *testing.T) {
	set := mustSet(t, ruleJSON("X", "1.0", ""), ruleJSON("Y", "1.0", ""), ruleJSON("Z", "1.0", ""))
	m1 := mirror(t, indexJSON("X", "2.0", "Y", "2.0", "Z", "2.0", "../evil", "9.9"), map[string]string{
		"X.json": ruleJSON("X", "1.9", ""), // 清单写 2.0，文件却是 1.9（镜像缓存没同步）
		"Y.json": ruleJSON("Y", "2.0", `,"api":"10"`),
		"Z.json": ruleJSON("Other", "2.0", ""),
	})
	m2 := mirror(t, indexJSON("X", "2.0", "Y", "1.0", "Z", "1.0"), map[string]string{
		"X.json": ruleJSON("X", "2.0", ""),
	})
	s, dir := newSyncer(t, set, m1, m2)
	st := s.Check(context.Background())

	if version(set, "X") != "2.0" {
		t.Errorf("X 应该换成另一个镜像上版本对得上的那份: v%s", version(set, "X"))
	}
	if version(set, "Y") != "1.0" || version(set, "Z") != "1.0" {
		t.Errorf("Y（引擎太旧）、Z（名字对不上）应保留旧版: %+v", set.Infos())
	}
	if len(st.Skipped) != 2 {
		t.Errorf("应记下 2 条没换成的: %+v", st.Skipped)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.json")); err == nil {
		t.Error("非法规则名不能写到规则目录外面")
	}
}

// 清单里一下子少了一大半的站：多半是镜像出错，不删
func TestSyncMassDeleteGuard(t *testing.T) {
	set := mustSet(t, ruleJSON("A", "1", ""), ruleJSON("B", "1", ""), ruleJSON("C", "1", ""), ruleJSON("D", "1", ""))
	idx := indexJSON("A", "1")
	s, _ := newSyncer(t, set, mirror(t, idx, nil), mirror(t, idx, nil))
	st := s.Check(context.Background())
	if len(set.All()) != 4 || st.Note == "" || len(st.Removed) != 0 {
		t.Fatalf("不该删: %+v %+v", set.Infos(), st)
	}
}

// 上游把名字的大小写改了：旧文件要删掉，不能在盘上留两份
func TestSyncNameCaseChange(t *testing.T) {
	set := mustSet(t, ruleJSON("age", "1.5", ""))
	m := mirror(t, indexJSON("AGE", "1.6"), map[string]string{"AGE.json": ruleJSON("AGE", "1.6", "")})
	s, dir := newSyncer(t, set, m, m)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "age.json"), []byte(ruleJSON("age", "1.5", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Check(context.Background())
	disk := rules.Load(dir, nil)
	if len(disk.All()) != 1 || version(disk, "age") != "1.6" {
		t.Fatalf("盘上应只剩新版一份: %+v", disk.Infos())
	}
}

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1", "1.0", 0, true},
		{"1.10", "1.9", 1, true},
		{"2.3", "2.4", -1, true},
		{"1.a", "1.0", 0, false},
		{"", "1", 0, false},
	}
	for _, c := range cases {
		if got, ok := compareVersion(c.a, c.b); got != c.want || ok != c.ok {
			t.Errorf("compareVersion(%q,%q) = %d,%v 想要 %d,%v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
	if newer("abc", "1.0") || !newer("1.2", "abc") || newer("1.0", "1") {
		t.Error("newer 判断不对")
	}
}

func TestUntilDaily(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 2, h, m, 0, 0, beijing) }
	if d := untilDaily(at(3, 0), 20); d != 80*time.Minute {
		t.Errorf("凌晨 3 点应等到 4:20: %v", d)
	}
	if d := untilDaily(at(4, 20), 20); d != 24*time.Hour {
		t.Errorf("正好 4:20 应等到明天: %v", d)
	}
	if d := untilDaily(at(14, 0).UTC(), 20); d != 14*time.Hour+20*time.Minute {
		t.Errorf("时区换算不对: %v", d)
	}
}

// 清单里有站名、版本号却一个都看不懂：这份清单不可信，算这个镜像失败，不能拿它删站
func TestSyncIndexWithoutVersionsDeletesNothing(t *testing.T) {
	set := mustSet(t, ruleJSON("A", "1", ""), ruleJSON("B", "1", ""), ruleJSON("C", "1", ""), ruleJSON("D", "1", ""))
	bad := indexJSON("A", "latest", "B", "")
	s, _ := newSyncer(t, set, mirror(t, bad, nil), mirror(t, bad, nil))
	st := s.Check(context.Background())
	if len(set.All()) != 4 || len(st.Removed) != 0 || st.LastOK {
		t.Fatalf("看不懂的清单不能用来删站: rules=%d removed=%+v status=%+v", len(set.All()), st.Removed, st)
	}
	if st.Sources[0].OK || !strings.Contains(st.Sources[0].Error, "版本号") {
		t.Errorf("镜像应标成失败并写明原因: %+v", st.Sources)
	}
}

// 该换的规则因为网络没下载下来、或者有镜像没连上：一小时后再查，不等到明天；
// 版本对不上这种等多久都没用的，照常等明天
func TestSyncRetrySoon(t *testing.T) {
	ctx := context.Background()

	set := mustSet(t, ruleJSON("A", "1.0", ""))
	s, _ := newSyncer(t, set, mirror(t, indexJSON("A", "2.0"), nil)) // 规则文件 404
	st, again := s.check(ctx)
	if !again || !st.LastOK || version(set, "A") != "1.0" || len(st.Skipped) != 1 {
		t.Fatalf("下载失败应保住旧版并一小时后重试: again=%v %+v", again, st)
	}

	set = mustSet(t, ruleJSON("A", "1.0", ""))
	s, _ = newSyncer(t, set, mirror(t, indexJSON("A", "2.0"), map[string]string{"A.json": ruleJSON("A", "1.9", "")}))
	if _, again := s.check(ctx); again {
		t.Error("文件版本对不上，过一小时也不会变，不该反复重试")
	}

	set = mustSet(t, ruleJSON("A", "1.0", ""))
	s, _ = newSyncer(t, set, mirror(t, indexJSON("A", "1.0"), nil), mirror(t, "", nil))
	if st, again := s.check(ctx); !again || !st.LastOK {
		t.Errorf("有一个镜像没连上应一小时后再查: again=%v %+v", again, st)
	}

	set = mustSet(t, ruleJSON("A", "1.0", ""))
	s, _ = newSyncer(t, set, mirror(t, indexJSON("A", "1.0"), nil))
	if _, again := s.check(ctx); again {
		t.Error("一切正常就等明天")
	}
}
