package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config 服务配置，默认值见 defaultConfig，可用 config.json 覆盖（与程序同目录）
type Config struct {
	Port        string         `json:"port"`
	MovieRoot   string         `json:"movieRoot"`
	SeriesRoot  string         `json:"seriesRoot"`
	DownloadDir string         `json:"downloadDir"`
	CacheDir    string         `json:"cacheDir"` // BT 缓存/未完成下载目录
	ListenAddr  string         `json:"listenAddr"`
	CAFile      string         `json:"caFile"`      // 自定义 CA 证书 bundle（Android 无系统 CA 时需要）
	Proxy       ProxyConfig    `json:"proxy"`       // 可选的自托管代理（用于 mikan）
	Sources     SourceSettings `json:"sources"`     // 各搜索源开关
	SearchTTL   int            `json:"searchTTL"`   // 聚合搜索缓存秒数
	Speedtest   int            `json:"speedtest"`   // 测速采样秒数
	BTPort      int            `json:"btPort"`      // BT 监听端口（固定值便于在路由器做端口映射）
	BTNoDHT     bool           `json:"btNoDHT"`     // 关闭 DHT（只靠 tracker）
	BTDebug     bool           `json:"btDebug"`     // 打开 BT 引擎调试日志（排查 peers 问题用）
	BTUploadKBs int            `json:"btUploadKBs"` // 上行限速（KB/s）。BT 是互惠协议，不上传会被对端限速甚至踢掉；0 表示不限速
	BTMaxConns  int            `json:"btMaxConns"`  // 单个种子的已建立连接上限
	BTUPnP      bool           `json:"btUPnP"`      // 自动向路由器申请端口映射，让 peer 能主动连进来
	Anime       AnimeConfig    `json:"anime"`       // 番剧聚合（Bangumi + 规则 + 播放代理）
}

// AnimeConfig 番剧聚合配置。目录留空表示放在可执行文件同目录下（rules/、anime-cache/），
// 这样开发机和盒子不用各写一套路径。
type AnimeConfig struct {
	Enabled    bool   `json:"enabled"`
	RulesDir   string `json:"rulesDir"`   // 规则 JSON 目录；没有时回退到编译进二进制的规则
	CacheDir   string `json:"cacheDir"`   // Bangumi 元数据与封面缓存
	ImgCacheMB int    `json:"imgCacheMB"` // 封面缓存总量上限
	BangumiUA  string `json:"bangumiUA"`  // 访问 Bangumi 时的 User-Agent
}

type ProxyConfig struct {
	Base  string `json:"base"`  // 例如 http://proxy.example.com:10808
	Token string `json:"token"` // X-Proxy-Token 值
}

type SourceSettings struct {
	Dytt8899 bool `json:"dytt8899"`
	Dygangs  bool `json:"dygangs"`
	Mp4z6    bool `json:"mp4z6"`
	Mikan    bool `json:"mikan"`
}

func defaultConfig() Config {
	return Config{
		Port:        "8088",
		MovieRoot:   "/mnt/sda/sda1/电影电视剧",
		SeriesRoot:  "/mnt/sda/sda1/番剧",
		DownloadDir: "/mnt/sda/sda1/下载",
		// 缓存放在硬盘上，和媒体目录同一个文件系统：入库时是瞬间改名而不是整片复制，
		// 也不受内置 /data 分区只有 4.9G 的限制（大片动辄 20G+）
		CacheDir:   "/mnt/sda/sda1/.torrent-cache",
		ListenAddr: "",
		Proxy:      ProxyConfig{},
		Sources: SourceSettings{
			Dytt8899: true,
			Dygangs:  true,
			Mp4z6:    true,
			Mikan:    true,
		},
		SearchTTL:   300,
		Speedtest:   8,
		BTPort:      51413, // 固定端口，便于在路由器做端口映射
		BTNoDHT:     false,
		BTDebug:     false,
		BTUploadKBs: 2048, // 2 MB/s：千兆宽带下毫无压力，换来对端不掐我们的下载
		BTMaxConns:  80,
		BTUPnP:      true,
		Anime:       AnimeConfig{Enabled: true, ImgCacheMB: 300},
	}
}

func loadConfig() Config {
	cfg := defaultConfig()
	// 优先读与可执行文件同目录的 config.json
	exe, err := os.Executable()
	dir := "."
	if err == nil {
		dir = filepath.Dir(exe)
	}
	for _, p := range []string{filepath.Join(dir, "config.json"), "config.json"} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if json.Unmarshal(data, &cfg) == nil {
			return cfg
		}
	}
	return cfg
}
