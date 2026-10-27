// rescue-server —— 机顶盒救援服务
//
// 独立于媒体服务（8088）的运维小服务，监听 8089。
// 媒体服务挂掉、网页打不开时，手机浏览器访问 http://<盒子IP>:8089
// 依然可以一键修复：启停/重启服务、查看日志、扫描媒体库、
// 拔插硬盘后修复（自动嫁接挂载点 + 重建损坏的 BT 数据库）、查看任务与清缓存。
//
// 设计约束：盒子是 Android 4.4（API 19）、ARMv7，shell 为精简 mksh，
// 没有 head/cut，Unix 工具一律走 /system/bin/busybox。
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML string

//go:embed toolbox.css
var toolboxCSS string

//go:embed toolbox.js
var toolboxJS string

const (
	port       = "8089"
	mediactl   = "/data/server/bin/mediactl"
	mediaMount = "/mnt/sda/sda1"
	cacheDir   = mediaMount + "/.torrent-cache"
	logFile    = "/data/server/logs/media-server.log"
	busybox    = "/system/bin/busybox"
	httpHost   = "http://127.0.0.1:8088"
)

// 固定别名是可选功能；地址由部署者通过环境变量设置。
var (
	aliasIP   = strings.TrimSpace(os.Getenv("BOX_ALIAS_IP"))
	aliasCIDR = aliasIP + "/24"
	serviceIP = strings.TrimSpace(os.Getenv("BOX_SERVICE_IP"))
)

// 一次修复动作只能同时跑一个，避免手机端连点导致叠加嫁接
var repairMu sync.Mutex

// ---------- shell 封装 ----------

func sh(cmd string, timeout time.Duration) (bool, string) {
	c := exec.Command("/system/bin/sh", "-c", cmd)
	c.Env = []string{"PATH=/system/bin:/system/xbin", "HOME=/data"}
	out, err := runWithTimeout(c, timeout)
	if err != nil {
		return false, strings.TrimSpace(out + "\n" + err.Error())
	}
	return true, strings.TrimSpace(out)
}

func runWithTimeout(c *exec.Cmd, timeout time.Duration) (string, error) {
	done := make(chan error, 1)
	var buf strings.Builder
	c.Stdout = &buf
	c.Stderr = &buf
	if err := c.Start(); err != nil {
		return buf.String(), err
	}
	go func() { done <- c.Wait() }()
	select {
	case <-time.After(timeout):
		_ = c.Process.Kill()
		<-done
		return buf.String(), fmt.Errorf("命令超时(%v)", timeout)
	case err := <-done:
		return buf.String(), err
	}
}

// ---------- 业务动作 ----------

type svcStatus struct {
	Running bool   `json:"running"`
	Pid     int    `json:"pid"`
	HTTP    bool   `json:"http"`
	Lib     string `json:"lib"`
}

func svcStatusNow() svcStatus {
	s := svcStatus{}
	ok, out := sh(mediactl+" status", 10*time.Second)
	if m := regexp.MustCompile(`pid (\d+)`).FindStringSubmatch(out); ok && m != nil {
		s.Running = true
		s.Pid, _ = strconv.Atoi(m[1])
	}
	// 网页是否可访问 + 库里有多少部（媒体服务没响应时这里就是 false）
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(httpHost + "/api/library?cat=all")
	if err == nil {
		defer resp.Body.Close()
		s.HTTP = true
		var d struct {
			Categories []struct {
				ID    string `json:"id"`
				Count int    `json:"count"`
			} `json:"categories"`
		}
		if json.NewDecoder(resp.Body).Decode(&d) == nil {
			var all, movie, series int
			for _, c := range d.Categories {
				switch c.ID {
				case "all":
					all = c.Count
				case "movie":
					movie = c.Count
				case "series":
					series = c.Count
				}
			}
			s.Lib = fmt.Sprintf("共 %d 部（电影电视剧 %d · 番剧 %d）", all, movie, series)
		}
	}
	return s
}

func svcAction(action string) (string, error) {
	switch action {
	case "start", "stop", "restart":
	default:
		return "", fmt.Errorf("不支持的动作")
	}
	if action == "start" || action == "restart" {
		// 缓存目录可能随硬盘掉落消失，启动前补一次
		sh("mkdir -p "+cacheDir, 10*time.Second)
	}
	ok, out := sh(mediactl+" "+action, 60*time.Second)
	if !ok {
		return out, fmt.Errorf("执行失败")
	}
	if action != "stop" {
		// 等网页就绪，最多 30s
		client := &http.Client{Timeout: 3 * time.Second}
		for i := 0; i < 10; i++ {
			time.Sleep(3 * time.Second)
			if resp, err := client.Get(httpHost + "/api/library?cat=all"); err == nil {
				resp.Body.Close()
				out += fmt.Sprintf("\n服务已就绪（等待 %ds）", (i+1)*3)
				return out, nil
			}
		}
		out += "\n30 秒内网页仍无响应，请看日志"
	}
	return out, nil
}

func readLogs() string {
	_, out := sh(busybox+" tail -n 120 "+logFile, 20*time.Second)
	return out
}

func scanLibrary() error {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(httpHost+"/api/scan", "application/json", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("媒体服务无响应：%v", err)
	}
	resp.Body.Close()
	return nil
}

type mountPoint struct {
	Dev  string `json:"dev"`
	Path string `json:"path"`
}

func listUSBMounts() []mountPoint {
	_, out := sh("mount | grep '/mnt/sd'", 10*time.Second)
	re := regexp.MustCompile(`(/dev/block/\S+) (/mnt/sd[a-z]+/sd[a-z]+1) `)
	ms := re.FindAllStringSubmatch(out, -1)
	seen := map[string]bool{}
	var res []mountPoint
	for _, m := range ms {
		k := m[1] + m[2]
		if !seen[k] {
			seen[k] = true
			res = append(res, mountPoint{Dev: m[1], Path: m[2]})
		}
	}
	return res
}

// diskRepair 拔插硬盘后的一键修复，逻辑与 Windows 面板 box_panel.py 的
// disk_recover 保持一致：
//  1. 找到 USB 盘实际挂载点；不是老路径且老路径不可用时，bind 嫁接回老路径
//  2. 检测掉盘写坏的 BT 数据库（invalid database），无下载任务则重建
//  3. 补建缓存目录并重启媒体服务
func diskRepair(logf func(string)) (string, error) {
	repairMu.Lock()
	defer repairMu.Unlock()

	mounts := listUSBMounts()
	if len(mounts) == 0 {
		return "", fmt.Errorf("没找到已挂载的 USB 硬盘，请重新插一次，等十几秒再点修复")
	}
	usb := mounts[0].Path
	logf("硬盘挂载点：" + usb)
	if usb != mediaMount {
		if hasMediaDirs() {
			logf("老路径已可用，跳过嫁接")
		} else {
			logf("盘改名字了（期望 " + mediaMount + "），正在嫁接回老路径…")
			sh("mkdir -p "+mediaMount, 10*time.Second)
			if ok, out := sh("mount -o bind "+usb+" "+mediaMount, 15*time.Second); !ok {
				return out, fmt.Errorf("嫁接失败：%s", out)
			}
			logf("嫁接完成，正在校验媒体目录…")
			if !hasMediaDirs() {
				return "", fmt.Errorf("嫁接后老路径里没有媒体目录，先别用，把页面上显示的内容发出来看看")
			}
		}
	}

	// 坏库检测：只看最近一次服务启动之后的日志段，避免旧记录误报
	startLine := 0
	if ok, mark := sh("grep -n 'starting media-server on port' "+logFile+" | "+busybox+" tail -n 1", 15*time.Second); ok && mark != "" {
		if m := regexp.MustCompile(`^(\d+):`).FindStringSubmatch(strings.TrimSpace(mark)); m != nil {
			startLine, _ = strconv.Atoi(m[1])
		}
	}
	damaged := false
	if startLine > 0 {
		ok, cnt := sh(busybox+fmt.Sprintf(" tail -n +%d ", startLine)+logFile+" | grep -c 'invalid database'", 15*time.Second)
		damaged = ok && cnt != "" && cnt != "0"
	}
	if damaged {
		busy := false
		client := &http.Client{Timeout: 5 * time.Second}
		if resp, err := client.Get(httpHost + "/api/tasks"); err == nil {
			var tasks []struct {
				Status string `json:"status"`
			}
			if json.NewDecoder(resp.Body).Decode(&tasks) == nil {
				for _, t := range tasks {
					if t.Status == "downloading" {
						busy = true
					}
				}
			}
			resp.Body.Close()
		}
		if busy {
			logf("BT 数据库曾损坏，但当前有下载任务，本次跳过重建，下载完再修")
		} else {
			logf("检测到 BT 进度数据库损坏，正在重建…")
			sh("rm -f "+cacheDir+"/.torrent.bolt.db", 10*time.Second)
			logf("数据库已删除，重启服务后会自动重建")
		}
	}

	logf("补建缓存目录并重启服务…")
	sh("mkdir -p "+cacheDir, 10*time.Second)
	out, err := svcAction("restart")
	return out, err
}

func listTasks() (interface{}, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(httpHost + "/api/tasks")
	if err != nil {
		return nil, fmt.Errorf("媒体服务无响应")
	}
	defer resp.Body.Close()
	var d interface{}
	return d, json.NewDecoder(resp.Body).Decode(&d)
}

func listCache() (interface{}, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(httpHost + "/api/manage/cache")
	if err != nil {
		return nil, fmt.Errorf("媒体服务无响应")
	}
	defer resp.Body.Close()
	var d interface{}
	return d, json.NewDecoder(resp.Body).Decode(&d)
}

func clearCache() (interface{}, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Post(httpHost+"/api/manage/cache/delete", "application/json", strings.NewReader(`{"all":true}`))
	if err != nil {
		return nil, fmt.Errorf("媒体服务无响应")
	}
	defer resp.Body.Close()
	var d interface{}
	return d, json.NewDecoder(resp.Body).Decode(&d)
}

func diskInfo() string {
	_, out := sh("df "+mediaMount+" /data", 15*time.Second)
	return out
}

// hasAliasIP 检查盒子上是否已经挂着配置的别名地址
func hasAliasIP() bool {
	if net.ParseIP(aliasIP) == nil {
		return false
	}
	_, out := sh("ip addr show", 10*time.Second)
	return strings.Contains(out, aliasIP+"/")
}

// currentIPs 返回盒子当前全部 IPv4 地址
func currentIPs() []string {
	_, out := sh("ip -4 addr show", 10*time.Second)
	var ips []string
	for _, m := range regexp.MustCompile(`inet (\d+\.\d+\.\d+\.\d+)/\d+`).FindAllStringSubmatch(out, -1) {
		if m[1] != "127.0.0.1" {
			ips = append(ips, m[1])
		}
	}
	return ips
}

// activeIface 找到挂着默认路由的网卡（IP 挂它上面才保得住外网和局域网）
func activeIface() string {
	_, out := sh("ip route show", 10*time.Second)
	m := regexp.MustCompile(`default via \S+ dev (\S+)`).FindStringSubmatch(out)
	if m != nil {
		return m[1]
	}
	return "wlan0"
}

// applyAliasIP 把配置的别名挂回网卡（幂等：已挂着就直接说 OK）
func applyAliasIP() (string, error) {
	if net.ParseIP(aliasIP) == nil {
		return "", fmt.Errorf("请通过 BOX_ALIAS_IP 配置一个未被占用的局域网 IPv4 地址")
	}
	iface := activeIface()
	if hasAliasIP() {
		return "别名 " + aliasIP + " 已在位（网卡 " + iface + "）", nil
	}
	ok, out := sh("ip addr add "+aliasCIDR+" dev "+iface, 10*time.Second)
	if !ok {
		return out, fmt.Errorf("挂载别名失败：%s", out)
	}
	// 挂完自检一次
	if hasAliasIP() {
		return "已把 " + aliasIP + " 挂回网卡 " + iface + "，老地址恢复可用", nil
	}
	return out, fmt.Errorf("挂载命令执行了但自检失败，请重试一次")
}

// ---------- 一键体检 ----------
//
// 逐项检查盒子健康状况，并给出「该点哪个按钮」的建议。
// 手机工具箱（index.html）和 Windows 面板（box_panel.py）都调这一个接口，
// 诊断逻辑只维护一份。

// check 单项体检结果
type check struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`      // 推荐动作 key，前端映射到按钮
	FixLabel string `json:"fixLabel,omitempty"` // 推荐按钮的文案
}

// libCount 网页可达性 + 媒体库各分类数量
type libCount struct {
	Reach  bool
	All    int
	Movie  int
	Series int
}

func fetchLibrary() libCount {
	info := libCount{}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(httpHost + "/api/library?cat=all")
	if err != nil {
		return info
	}
	defer resp.Body.Close()
	info.Reach = true
	var d struct {
		Categories []struct {
			ID    string `json:"id"`
			Count int    `json:"count"`
		} `json:"categories"`
	}
	if json.NewDecoder(resp.Body).Decode(&d) != nil {
		return info
	}
	for _, c := range d.Categories {
		switch c.ID {
		case "all":
			info.All = c.Count
		case "movie":
			info.Movie = c.Count
		case "series":
			info.Series = c.Count
		}
	}
	return info
}

// checkMount 硬盘挂载体检。要分清三种情况：
//   - 盘改名了（拔插后系统换了设备号，挂载表还盯着旧门牌）——本次真实故障
//   - 盘没插好 / 没挂载
//   - 挂上了但 NTFS 索引损坏（要拿去 Windows 跑 chkdsk）
func checkMount() check {
	c := check{ID: "mount", Name: "硬盘挂载"}
	_, mnt := sh("grep ' "+mediaMount+" ' /proc/mounts", 5*time.Second)
	if strings.TrimSpace(mnt) == "" {
		c.Detail = "硬盘没挂载到 " + mediaMount + "，可能没插好或系统没认出来"
		c.Fix, c.FixLabel = "disk_repair", "拔插硬盘后一键修复"
		return c
	}
	src := strings.Fields(mnt)[0]
	if hasMediaDirs() {
		c.OK = true
		c.Detail = "正常（设备 " + src + "）"
		return c
	}
	// 目录读不出来：再探设备本身，分清是设备失效还是文件系统损坏
	_, dd := sh("dd if="+src+" of=/dev/null bs=4096 count=1", 10*time.Second)
	switch {
	case strings.Contains(dd, "No such device"), strings.Contains(dd, "No such file"):
		c.Detail = "挂载表还盯着旧设备 " + src + "，但那扇门已经拆了——拔插后系统给盘改了名字，服务找不到它"
	case strings.Contains(dd, "Input/output error"):
		c.Detail = "设备连得上但目录读取报 I/O 错误，NTFS 索引可能又损坏了（需要拿去 Windows 跑 chkdsk 修复）"
	default:
		c.Detail = "设备 " + src + " 连得上，但媒体目录读不到（电影电视剧 / 番剧）"
	}
	c.Fix, c.FixLabel = "disk_repair", "拔插硬盘后一键修复"
	return c
}

// hasMediaDirs 判断媒体目录在不在。
//
// 注意：必须用 test -d，绝不能 grep ls 的输出——盒子 locale 不支持 UTF-8，
// busybox ls 会把中文目录名输出成一串问号（"??????"），
// 用中文去匹配 ls 结果必然失败，会把正常的盘误判成坏的。
// test -d 走内核路径比对，不经过输出转码，结果才可靠。
func hasMediaDirs() bool {
	ok, _ := sh("test -d "+mediaMount+"/电影电视剧 || test -d "+mediaMount+"/番剧", 5*time.Second)
	// test 成功（退出码 0）= 至少一个媒体目录存在；sh() 的第一个返回值即成败
	return ok
}

// checkSvc 媒体服务进程是否活着
func checkSvc() check {
	c := check{ID: "svc", Name: "媒体服务进程"}
	ok, out := sh(mediactl+" status", 10*time.Second)
	if ok && strings.Contains(out, "running") {
		c.OK = true
		c.Detail = strings.TrimSpace(strings.Split(out, "\n")[0])
		return c
	}
	c.Detail = "media-server 没在运行"
	c.Fix, c.FixLabel = "svc_start", "启动服务"
	return c
}

// checkWeb 网页 8088 有没有响应
func checkWeb() check {
	c := check{ID: "web", Name: "网页 8088 响应"}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(httpHost + "/api/web-manifest")
	if err != nil {
		c.Detail = "无响应（" + err.Error() + "）"
		c.Fix, c.FixLabel = "svc_restart", "重启服务"
		return c
	}
	resp.Body.Close()
	c.OK = true
	c.Detail = "正常"
	return c
}

// checkLib 媒体库数量。盘正常却是 0 部，就是单纯没扫。
func checkLib() check {
	c := check{ID: "lib", Name: "媒体库"}
	info := fetchLibrary()
	if !info.Reach {
		c.Detail = "网页不通，读不到库"
		c.Fix, c.FixLabel = "svc_restart", "重启服务"
		return c
	}
	if info.All == 0 {
		c.Detail = "扫出 0 部——硬盘正常的话就是没扫到"
		c.Fix, c.FixLabel = "scan", "重新扫描媒体库"
		return c
	}
	c.OK = true
	c.Detail = fmt.Sprintf("共 %d 部（电影电视剧 %d · 番剧 %d）", info.All, info.Movie, info.Series)
	return c
}

// checkAlias 别名地址还在不在（掉了老书签就打不开）
func checkAlias() check {
	c := check{ID: "alias", Name: "老地址 " + aliasIP}
	if net.ParseIP(aliasIP) == nil {
		c.Name = "固定地址（可选）"
		c.OK = true
		c.Detail = "未配置 BOX_ALIAS_IP，跳过固定地址检查"
		return c
	}
	if hasAliasIP() {
		c.OK = true
		c.Detail = "在位"
		return c
	}
	c.Detail = "掉了，盒子现在只有 " + strings.Join(currentIPs(), "、") + "（老书签会打不开）"
	c.Fix, c.FixLabel = "fix_ip", "一键改回 "+aliasIP
	return c
}

// checkConflict 老地址有没有被局域网里别的设备抢走（两台机器抢一个 IP 会打架）
func checkConflict() check {
	c := check{ID: "conflict", Name: "老地址是否被抢"}
	if net.ParseIP(aliasIP) == nil {
		c.Name = "固定地址冲突（可选）"
		c.OK = true
		c.Detail = "未配置固定地址，跳过检查"
		return c
	}
	if !hasAliasIP() {
		c.OK = true
		c.Detail = "老地址不在位，先按上一项挂回来再看"
		return c
	}
	iface := activeIface()
	_, own := sh("cat /sys/class/net/"+iface+"/address", 5*time.Second)
	own = strings.ToLower(strings.TrimSpace(own))
	_, neigh := sh("ip neigh show "+aliasIP, 5*time.Second)
	m := regexp.MustCompile(`lladdr ([0-9a-fA-F:]{17})`).FindStringSubmatch(neigh)
	if m == nil || own == "" {
		c.OK = true
		c.Detail = "邻居表里没有别的设备占着，未见冲突"
		return c
	}
	other := strings.ToLower(m[1])
	if other == own {
		c.OK = true
		c.Detail = "归盒子自己（MAC " + own + "）"
		return c
	}
	c.Detail = fmt.Sprintf("被别的设备占了（对方 MAC %s，盒子 %s）——访问 .13 会被带去别人家，得用当前真实地址", other, own)
	c.Fix, c.FixLabel = "scan_hosts", "找到盒子（确认真实地址）"
	return c
}

// diagnose 一键体检。检查顺序就是问题优先级：盘 → 进程 → 网页 → 库 → 网络，
// 推荐按钮取第一个出问题的项。
func diagnose() map[string]interface{} {
	checks := []check{
		checkMount(),
		checkSvc(),
		checkWeb(),
		checkLib(),
		checkAlias(),
		checkConflict(),
	}
	var bad []check
	for _, c := range checks {
		if !c.OK {
			bad = append(bad, c)
		}
	}
	verdict := "一切正常，可以放心看片。"
	primary := map[string]string{}
	if n := len(bad); n > 0 {
		if bad[0].FixLabel != "" {
			verdict = fmt.Sprintf("发现 %d 个问题，建议先点「%s」", n, bad[0].FixLabel)
			primary["fix"] = bad[0].Fix
			primary["label"] = bad[0].FixLabel
		} else {
			verdict = fmt.Sprintf("发现 %d 个问题", n)
		}
	}
	return map[string]interface{}{
		"checks":   checks,
		"problems": len(bad),
		"verdict":  verdict,
		"primary":  primary,
		"now":      time.Now().Format("2006-01-02 15:04:05"),
	}
}

// scanHosts 探测局域网里有哪些设备开着盒子的特征端口（8088/8089），
// 用于 IP 漂移后找到盒子的真实地址。 arp 表 + 已知常见地址逐个试。
func scanHosts() []string {
	var found []string
	seen := map[string]bool{}
	ips := []string{serviceIP, aliasIP}
	// 从 ARP 表捞本网段已知主机
	if _, out := sh("cat /proc/net/arp", 10*time.Second); out != "" {
		for _, m := range regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+)\s`).FindAllStringSubmatch(out, -1) {
			ips = append(ips, m[1])
		}
	}
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	for _, ip := range ips {
		if ip == "" || seen[ip] || !strings.HasPrefix(ip, "192.168.1.") {
			continue
		}
		seen[ip] = true
		for _, p := range []string{"8088", "8089"} {
			// 探测特征：8088 影院用 /api/web-manifest，8089 救援用 /api/status
			feat := "/api/web-manifest"
			if p == "8089" {
				feat = "/api/status"
			}
			resp, err := client.Get("http://" + ip + ":" + p + feat)
			if err == nil {
				resp.Body.Close()
				found = append(found, ip+":"+p)
				break
			}
		}
	}
	return found
}

// ---------- HTTP 处理 ----------

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func main() {
	mux := http.NewServeMux()

	// 样式和脚本也内嵌到救援服务，媒体服务不可用时仍能完整打开工具箱。
	mux.HandleFunc("/toolbox.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(toolboxCSS))
	})
	mux.HandleFunc("/toolbox.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(toolboxJS))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(indexHTML))
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		s := svcStatusNow()
		writeJSON(w, map[string]interface{}{
			"svc":      s,
			"mounts":   listUSBMounts(),
			"disk":     diskInfo(),
			"now":      time.Now().Format("2006-01-02 15:04:05"),
			"ips":      currentIPs(),
			"hasAlias": hasAliasIP(),
			"aliasIP":  aliasIP,
		})
	})

	// 一键体检：逐项检查出问题的地方，并推荐该点哪个按钮
	mux.HandleFunc("/api/diagnose", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, diagnose())
	})

	// 扫描局域网找盒子（IP 漂移后用）
	mux.HandleFunc("/api/scan-hosts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"found": scanHosts()})
	})

	// 一键把老 IP（aliasIP）挂回盒子网卡
	mux.HandleFunc("/api/fix-ip", func(w http.ResponseWriter, r *http.Request) {
		out, err := applyAliasIP()
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "out": out, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "out": out})
	})

	mux.HandleFunc("/api/svc", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string `json:"action"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		out, err := svcAction(req.Action)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "out": out, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "out": out})
	})

	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"logs": readLogs()})
	})

	mux.HandleFunc("/api/scan", func(w http.ResponseWriter, r *http.Request) {
		if err := scanLibrary(); err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, map[string]string{"ok": "已触发扫描"})
	})

	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		d, err := listTasks()
		if err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, d)
	})

	mux.HandleFunc("/api/cache", func(w http.ResponseWriter, r *http.Request) {
		d, err := listCache()
		if err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, d)
	})

	mux.HandleFunc("/api/cache/clear", func(w http.ResponseWriter, r *http.Request) {
		d, err := clearCache()
		if err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, d)
	})

	// 拔插硬盘后的一键修复（耗时操作，SSE 推送进度日志）
	mux.HandleFunc("/api/repair", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, 500, "当前连接不支持流式输出")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		logf := func(msg string) {
			fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(msg, "\n", "\ndata: "))
			fl.Flush()
		}
		out, err := diskRepair(logf)
		if err != nil {
			fmt.Fprintf(w, "event: done\ndata: {\"ok\":false,\"error\":%q}\n\n", err.Error())
		} else {
			fmt.Fprintf(w, "event: done\ndata: {\"ok\":true,\"out\":%q}\n\n", out)
		}
		fl.Flush()
	})

	addr := net.JoinHostPort("", port)
	fmt.Printf("rescue-server listening on :%s\n", port)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Println("listen error:", err)
		os.Exit(1)
	}
}
