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

// ruleJSON 一条能用的 xpath 规则；extra 拼在最后（如 `,"api":"10"`，同名字段以后面的为准）
func ruleJSON(name, version, extra string) string {
	return fmt.Sprintf(`{"api":"1","name":%q,"version":%q,"searchURL":"https://x.test/s?q=@keyword",`+
		`"searchList":"//a","searchName":"//a","searchResult":"//a","chapterRoads":"//b","chapterResult":"//a"%s}`,
		name, version, extra)
}

// badXPath 选集表达式引擎不认（值没带引号），本地验收一定过不了
const badXPath = `,"chapterRoads":"//ol[@class*=tvt]"`

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// diskSet 先把规则写进 dir（像盒子上已经同步过一次那样），再按启动时的方式读回来
func diskSet(t *testing.T, dir string, jsons ...string) *rules.Set {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, j := range jsons {
		r, err := rules.ParseRule([]byte(j))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, r.Name+".json"), []byte(j), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return rules.Load(dir, nil)
}

func findSkip(st Status, name string) *Skip {
	for i := range st.Skipped {
		if st.Skipped[i].Name == name {
			return &st.Skipped[i]
		}
	}
	return nil
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
	return newSyncerAt(t, dir, set, srvs...), dir
}

// newSyncerAt 同 newSyncer，规则目录由调用方指定（测试先在里面放好"盒子上已有的规则"）
func newSyncerAt(t *testing.T, dir string, set *rules.Set, srvs ...*httptest.Server) *Syncer {
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
	})
}

func version(set *rules.Set, name string) string {
	if r := set.Get(name); r != nil {
		return string(r.Version)
	}
	return ""
}

// 一个镜像最新、一个落后：取版本高的那份，落后的不能把本地降级；新站加上、两边都不列的站删掉；
// 已有旧版时，要验证码的新版不再顶掉旧版（2026-11-03 起，原来是跟上游照换）；
// TvTFun 的 *= 写法经兼容改写后能用；整套写进硬盘，重启后读回来一样
func TestSyncFreshAndStaleMirrors(t *testing.T) {
	mutefun14 := ruleJSON("mutefun", "1.4", "")
	set := mustSet(t, ruleJSON("AGE", "1.5", ""), ruleJSON("MXdm", "2.4", ""), mutefun14, ruleJSON("old", "1.0", ""))
	fresh := mirror(t, indexJSON("AGE", "1.6", "MXdm", "2.4", "mutefun", "1.6", "TvTFun", "1.3"), map[string]string{
		"AGE.json":     ruleJSON("AGE", "1.6", `,"adBlocker":true`),
		"mutefun.json": ruleJSON("mutefun", "1.6", `,"antiCrawlerConfig":{"enabled":true}`),
		"TvTFun.json":  ruleJSON("TvTFun", "1.3", `,"chapterRoads":"//ol[@class*='tvt-watch-episodes']"`),
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
	if got := set.Get("mutefun").Unsupported(); version(set, "mutefun") != "1.4" || got != "" {
		t.Errorf("mutefun 要验证码的新版不该顶掉旧版: v%s %q", version(set, "mutefun"), got)
	}
	if sk := findSkip(st, "mutefun"); sk == nil || sk.Version != "1.6" || sk.Kind != "captcha" || sk.CurrentVersion != "1.4" {
		t.Errorf("应记下 mutefun 上游 1.6 要验证码、继续用 1.4: %+v", st.Skipped)
	}
	if len(st.Updated) != 1 || st.Updated[0].Name != "AGE" || len(st.Added) != 1 || len(st.Removed) != 1 || st.Removed[0].Name != "old" {
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
	if got := readFile(t, filepath.Join(dir, "mutefun.json")); got != mutefun14 {
		t.Errorf("盘上的 mutefun 应还是 1.4 原文: %s", got)
	}
	// 只有真换了的 AGE 留了上一版；新站 TvTFun、没换成的 mutefun 都没有
	prev, _ := os.ReadDir(filepath.Join(dir, rules.PreviousDir))
	if len(prev) != 1 || readFile(t, filepath.Join(dir, rules.PreviousDir, "AGE.json")) != ruleJSON("AGE", "1.5", "") {
		t.Errorf("previous 里应只有 AGE 1.5 的原文: %v", prev)
	}

	// 状态存盘：重新创建（相当于服务重启）后还能看到上次检查
	s2 := New(s.o)
	if got := s2.Status(); !got.LastCheck.Equal(st.LastCheck) || len(got.Added) != 1 || got.Running || findSkip(got, "mutefun") == nil {
		t.Errorf("状态没存住: %+v", got)
	}

	// 再查一轮：没有变化，最近变化还是上一轮的；mutefun 照样记着没换成，但不用一小时后重试
	st2, again := s.check(context.Background())
	if !st2.LastChanged.Equal(st.LastChanged) || st2.LastCheck.Before(st.LastCheck) {
		t.Errorf("没变化的一轮不该改最近变化: %+v", st2)
	}
	if again || findSkip(st2, "mutefun") == nil {
		t.Errorf("要验证码这种等多久都一样，不该一小时后重试: again=%v %+v", again, st2.Skipped)
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

	// 同一个版本：一个镜像没下载下来，另一个镜像下下来了但验收没过——同一版本等多久都一样
	set = mustSet(t, ruleJSON("A", "1.0", ""))
	s, _ = newSyncer(t, set, mirror(t, indexJSON("A", "2.0"), nil),
		mirror(t, indexJSON("A", "2.0"), map[string]string{"A.json": ruleJSON("A", "2.0", badXPath)}))
	st, again = s.check(ctx)
	if again || version(set, "A") != "1.0" {
		t.Errorf("验收没过的版本不该一小时后重试: again=%v v%s", again, version(set, "A"))
	}
	if sk := findSkip(st, "A"); sk == nil || sk.Kind != "invalid" {
		t.Errorf("应说清是表达式不认识（比\"下载失败\"更说明问题）: %+v", st.Skipped)
	}

	// 更新的 3.0 没下载下来，落后镜像的 2.0 验收没过：3.0 可能是好的，一小时后再试
	set = mustSet(t, ruleJSON("A", "1.0", ""))
	s, _ = newSyncer(t, set, mirror(t, indexJSON("A", "3.0"), nil),
		mirror(t, indexJSON("A", "2.0"), map[string]string{"A.json": ruleJSON("A", "2.0", badXPath)}))
	st, again = s.check(ctx)
	if !again {
		t.Error("更新的版本没下载下来，应一小时后再试")
	}
	if sk := findSkip(st, "A"); sk == nil || sk.Version != "3.0" || sk.Kind != "download" {
		t.Errorf("说的应是上游最新的 3.0 没下载下来，不能把 2.0 的原因安到 3.0 头上: %+v", st.Skipped)
	}
}

// 已有 A 1.0、上游 A 2.0 的选集表达式引擎不认：继续用 1.0，当前文件和上一版都不动，
// 也不去落后镜像拿能用的 1.5 顶替；同一轮里 B 照常更新
func TestSyncKeepsCurrentWhenCandidateInvalid(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "rules")
	a10, b10 := ruleJSON("A", "1.0", ""), ruleJSON("B", "1.0", "")
	set := diskSet(t, dir, a10, b10)
	prevA := filepath.Join(dir, rules.PreviousDir, "A.json")
	a09 := ruleJSON("A", "0.9", "")
	if err := os.MkdirAll(filepath.Dir(prevA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prevA, []byte(a09), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := mirror(t, indexJSON("A", "2.0", "B", "1.1"), map[string]string{
		"A.json": ruleJSON("A", "2.0", badXPath),
		"B.json": ruleJSON("B", "1.1", ""),
	})
	stale := mirror(t, indexJSON("A", "1.5", "B", "1.1"), map[string]string{
		"A.json": ruleJSON("A", "1.5", ""),
		"B.json": ruleJSON("B", "1.1", ""),
	})
	s := newSyncerAt(t, dir, set, fresh, stale)
	st, again := s.check(ctx)

	if version(set, "A") != "1.0" || version(set, "B") != "1.1" {
		t.Fatalf("A 应继续用 1.0、B 照常换成 1.1: %+v", set.Infos())
	}
	if got := readFile(t, filepath.Join(dir, "A.json")); got != a10 {
		t.Errorf("A 的当前文件不该被改: %s", got)
	}
	if got := readFile(t, prevA); got != a09 {
		t.Errorf("A 的上一版不该被改: %s", got)
	}
	if got := readFile(t, filepath.Join(dir, rules.PreviousDir, "B.json")); got != b10 {
		t.Errorf("B 换新版前应把 1.0 存成上一版: %s", got)
	}
	sk := findSkip(st, "A")
	if sk == nil || sk.Version != "2.0" || sk.CurrentVersion != "1.0" || sk.Kind != "invalid" || sk.Reason != "选集表达式暂未兼容" {
		t.Errorf("跳过记录不对: %+v", st.Skipped)
	}
	if again {
		t.Error("表达式不认识等多久都一样，不该一小时后重试")
	}
	if len(st.Updated) != 1 || st.Updated[0].Name != "B" {
		t.Errorf("这一轮只该更新 B: %+v", st.Updated)
	}
	disk := rules.Load(dir, nil)
	if version(disk, "A") != "1.0" || version(disk, "B") != "1.1" || len(disk.All()) != 2 {
		t.Errorf("重启后读回来的不对: %+v", disk.Infos())
	}
}

// 新站的第一版就不合格（表达式不认识 / 要验证码）：先不启用，只记下原因；别的新站照常加上
func TestSyncNewSiteInvalidNotEnabled(t *testing.T) {
	set := mustSet(t, ruleJSON("A", "1.0", ""), ruleJSON("B", "1.0", ""))
	m := mirror(t, indexJSON("A", "1.0", "B", "1.0", "N1", "1.0", "N2", "1.0", "N3", "1.0"), map[string]string{
		"N1.json": ruleJSON("N1", "1.0", badXPath),
		"N2.json": ruleJSON("N2", "1.0", ""),
		"N3.json": ruleJSON("N3", "1.0", `,"antiCrawlerConfig":{"enabled":true}`),
	})
	s, dir := newSyncer(t, set, m)
	st, again := s.check(context.Background())

	if set.Get("N1") != nil || set.Get("N3") != nil || version(set, "N2") != "1.0" {
		t.Fatalf("不合格的新站不该启用: %+v", set.Infos())
	}
	if sk := findSkip(st, "N1"); sk == nil || sk.Kind != "invalid" || sk.CurrentVersion != "" || sk.Version != "1.0" {
		t.Errorf("N1 跳过记录不对: %+v", st.Skipped)
	}
	if sk := findSkip(st, "N3"); sk == nil || sk.Kind != "captcha" || sk.CurrentVersion != "" {
		t.Errorf("N3 跳过记录不对: %+v", st.Skipped)
	}
	if len(st.Added) != 1 || st.Added[0].Name != "N2" || again {
		t.Errorf("应只新增 N2、不用重试: %+v again=%v", st.Added, again)
	}
	if _, err := os.Stat(filepath.Join(dir, "N1.json")); err == nil {
		t.Error("不合格的新站不该写进硬盘")
	}
	if _, err := os.Stat(filepath.Join(dir, rules.PreviousDir)); err == nil {
		t.Error("没有站换过版本，不该有 previous")
	}
}

// 合格的更新：先把当前那份原文存成上一版再换；第二次更新时轮换，每站只留一份
func TestSyncPreviousRotates(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "rules")
	a1, a2, a3 := ruleJSON("A", "1.0", ""), ruleJSON("A", "2.0", `,"adBlocker":true`), ruleJSON("A", "3.0", "")
	set := diskSet(t, dir, a1)
	prevA := filepath.Join(dir, rules.PreviousDir, "A.json")

	newSyncerAt(t, dir, set, mirror(t, indexJSON("A", "2.0"), map[string]string{"A.json": a2})).Check(ctx)
	if version(set, "A") != "2.0" || readFile(t, filepath.Join(dir, "A.json")) != a2 || readFile(t, prevA) != a1 {
		t.Fatalf("第一次更新后应是 2.0、上一版 1.0: v%s", version(set, "A"))
	}
	newSyncerAt(t, dir, set, mirror(t, indexJSON("A", "3.0"), map[string]string{"A.json": a3})).Check(ctx)
	if version(set, "A") != "3.0" || readFile(t, filepath.Join(dir, "A.json")) != a3 || readFile(t, prevA) != a2 {
		t.Fatalf("第二次更新后应是 3.0、上一版 2.0: v%s", version(set, "A"))
	}
	if prev, _ := os.ReadDir(filepath.Dir(prevA)); len(prev) != 1 {
		t.Errorf("每站只留一份上一版: %v", prev)
	}
}

// 上一版存不进去（previous 被一个同名文件占着）：整轮不换，不能没留后路就把当前版覆盖掉
func TestSyncPreviousWriteFailAborts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")
	a1 := ruleJSON("A", "1.0", "")
	set := diskSet(t, dir, a1)
	if err := os.WriteFile(filepath.Join(dir, rules.PreviousDir), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSyncerAt(t, dir, set, mirror(t, indexJSON("A", "2.0"), map[string]string{"A.json": ruleJSON("A", "2.0", "")}))
	st := s.Check(context.Background())
	if st.LastOK || !strings.Contains(st.Error, "上一版") {
		t.Errorf("应报存上一版失败: %+v", st)
	}
	if version(set, "A") != "1.0" || readFile(t, filepath.Join(dir, "A.json")) != a1 {
		t.Errorf("当前版不该被换: v%s", version(set, "A"))
	}
}

// 上游确认下架：当前文件和上一版一起删，上一版不能让它"复活"
func TestSyncRemovedSiteClearsPrevious(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")
	set := diskSet(t, dir, ruleJSON("A", "1.0", ""), ruleJSON("B", "1.0", ""), ruleJSON("C", "1.0", ""))
	prevC := filepath.Join(dir, rules.PreviousDir, "C.json")
	if err := os.MkdirAll(filepath.Dir(prevC), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prevC, []byte(ruleJSON("C", "0.9", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	m := mirror(t, indexJSON("A", "1.0", "B", "1.0"), nil)
	st := newSyncerAt(t, dir, set, m, m).Check(context.Background())

	if len(st.Removed) != 1 || st.Removed[0].Name != "C" || set.Get("C") != nil {
		t.Fatalf("C 应该下架: %+v", st)
	}
	if _, err := os.Stat(prevC); err == nil {
		t.Error("下架站的上一版应一起删掉")
	}
	// 就算有人留了个坏掉的 C.json，也没有上一版可以拿来顶上
	if err := os.WriteFile(filepath.Join(dir, "C.json"), []byte(`{"name":"C",`), 0o644); err != nil {
		t.Fatal(err)
	}
	if disk := rules.Load(dir, nil); disk.Get("C") != nil || len(disk.All()) != 2 {
		t.Errorf("下架的站不该重新出现: %+v", disk.Infos())
	}
}
