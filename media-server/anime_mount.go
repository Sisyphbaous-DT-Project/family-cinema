package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	animeapi "media-server/internal/anime/api"
	animerules "media-server/internal/anime/rules"
)

// 编译进二进制的默认规则；运行时优先读磁盘上的 rules/ 目录（改规则只推文件，不用重编译）
//
//go:embed rules/*.json
var animeRulesFS embed.FS

// exeDir 可执行文件所在目录
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// mountAnime 把番剧聚合接口（/api/anime/*）挂到 mux 上。
// 配置里 anime.enabled 为 false（或没加载配置，如单元测试）时什么也不做。
func mountAnime(mux *http.ServeMux) {
	ac := currentConfig().Anime
	if !ac.Enabled {
		return
	}
	base := httpClient
	if base == nil {
		base = &http.Client{Timeout: 15 * time.Second}
	}
	// 播放代理转发的是视频流，不能用带总超时的 client；只共用它的 Transport（自定义 DNS、CA）
	stream := &http.Client{Transport: base.Transport}

	rulesDir := ac.RulesDir
	if rulesDir == "" {
		rulesDir = filepath.Join(exeDir(), "rules")
	}
	cacheDir := ac.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(exeDir(), "anime-cache")
	}
	embedded, err := fs.Sub(animeRulesFS, "rules")
	if err != nil {
		embedded = nil
	}
	set := animerules.Load(rulesDir, embedded)

	a := animeapi.New(animeapi.Deps{
		HTTP:       base,
		StreamHTTP: stream,
		LookupIP: func(ctx context.Context, host string) ([]net.IP, error) {
			return customDNS.LookupIP(ctx, "ip4", host)
		},
		Rules:      set,
		CacheDir:   cacheDir,
		ImgCacheMB: ac.ImgCacheMB,
		BangumiUA:  ac.BangumiUA,
	})
	a.Register(mux)
	a.Start(context.Background()) // 番剧播出时间、追番查更新
	initAnimeDownload(mux, a)     // 番剧"下载到盒子"
	log.Printf("番剧聚合已启用: 规则 %d 条, 缓存目录 %s", len(set.All()), cacheDir)
}
