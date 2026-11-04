package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	animeCoverMaxBytes = 12 << 20
	animeCoverTimeout  = 45 * time.Second
)

type animeCoverSelection struct {
	Source string `json:"source"`
	URL    string `json:"url"`
}

var animeCoverJobs struct {
	mu   sync.Mutex
	dirs map[string]bool
}

func validateAnimeCover(cover *animeCoverSelection) (*url.URL, error) {
	if cover == nil || cover.Source != "bangumi" {
		return nil, errors.New("封面来源无效")
	}
	u, err := url.Parse(strings.TrimSpace(cover.URL))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" ||
		(!strings.EqualFold(u.Host, "lain.bgm.tv") && !strings.EqualFold(u.Host, "lain.bgm.tv:443")) {
		return nil, errors.New("封面地址无效")
	}
	return u, nil
}

// startAnimeCover 与视频任务和提交请求的生命周期独立；失败后再次提交可以重试。
func startAnimeCover(dir string, cover *animeCoverSelection) bool {
	if cover == nil {
		return false
	}
	if _, err := validateAnimeCover(cover); err != nil {
		log.Printf("番剧封面未保存：%v", err)
		return false
	}
	if p := findCover(dir); p != "" {
		updateAnimeTaskCovers(dir, p)
		return false
	}
	key := filepath.Clean(dir)
	animeCoverJobs.mu.Lock()
	if animeCoverJobs.dirs[key] {
		animeCoverJobs.mu.Unlock()
		return false
	}
	if animeCoverJobs.dirs == nil {
		animeCoverJobs.dirs = make(map[string]bool)
	}
	animeCoverJobs.dirs[key] = true
	animeCoverJobs.mu.Unlock()
	selection := *cover
	go func() {
		defer func() {
			animeCoverJobs.mu.Lock()
			delete(animeCoverJobs.dirs, key)
			animeCoverJobs.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), animeCoverTimeout)
		defer cancel()
		p, published, err := saveAnimeCover(ctx, dir, &selection)
		if err != nil {
			log.Printf("番剧封面未保存：%v", err)
			return
		}
		updateAnimeTaskCovers(dir, p)
		if published {
			animeDLScanMu.Lock()
			ScanLibrary()
			animeDLScanMu.Unlock()
		}
	}()
	return true
}

func updateAnimeTaskCovers(dir, path string) {
	animeDL.mu.Lock()
	defer animeDL.mu.Unlock()
	for _, t := range animeDL.tasks {
		if absEqual(t.info.TargetDir, dir) {
			t.info.Cover = path
		}
	}
}

func fetchAnimeCover(ctx context.Context, cover *animeCoverSelection) ([]byte, string, error) {
	target, err := validateAnimeCover(cover)
	if err != nil {
		return nil, "", err
	}
	cfg := currentConfig()
	if cfg.Proxy.Base == "" {
		return nil, "", errors.New("图片代理未配置")
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(target.String()))
	req, err := newReq(http.MethodGet, strings.TrimRight(cfg.Proxy.Base, "/")+"/fetch?u="+encoded, nil)
	if err != nil {
		return nil, "", errors.New("图片代理地址无效")
	}
	req.Header.Set("Accept", "image/jpeg,image/png,image/gif")
	if cfg.Proxy.Token != "" {
		req.Header.Set("X-Proxy-Token", cfg.Proxy.Token)
	}
	client := *httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, "", errors.New("图片代理请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("图片代理返回 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, animeCoverMaxBytes+1))
	if err != nil {
		return nil, "", errors.New("图片读取失败")
	}
	if len(data) > animeCoverMaxBytes {
		return nil, "", errors.New("图片超过 12MiB")
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("图片内容无效")
	}
	switch format {
	case "jpeg":
		return data, ".jpg", nil
	case "png":
		return data, ".png", nil
	case "gif":
		// 片库现有封面名只认 jpg/png；GIF 取首帧存成真实 PNG，不扩大旧流程的格式规则。
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil || out.Len() > animeCoverMaxBytes {
			return nil, "", errors.New("GIF 封面转换失败")
		}
		return out.Bytes(), ".png", nil
	default:
		return nil, "", errors.New("图片格式暂不支持")
	}
}

// saveAnimeCover 只补缺图；自动补图与手动 saveCover 只在发布阶段共用短锁。
func saveAnimeCover(ctx context.Context, dir string, cover *animeCoverSelection) (path string, published bool, err error) {
	if p := findCover(dir); p != "" {
		return p, false, nil
	}
	data, ext, err := fetchAnimeCover(ctx, cover)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, errors.New("创建封面目录失败")
	}
	f, err := os.CreateTemp(dir, ".anime-cover-*")
	if err != nil {
		return "", false, errors.New("创建封面临时文件失败")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err := f.Chmod(0o644); err != nil {
		return "", false, errors.New("设置封面权限失败")
	}
	if _, err := f.Write(data); err != nil {
		return "", false, errors.New("写入封面临时文件失败")
	}
	if err := f.Close(); err != nil {
		return "", false, errors.New("保存封面临时文件失败")
	}
	coverPublishMu.Lock()
	defer coverPublishMu.Unlock()
	if p := findCover(dir); p != "" {
		return p, false, nil
	}
	p := filepath.Join(dir, "folder"+ext)
	if err := os.Rename(tmp, p); err != nil {
		return "", false, errors.New("发布封面失败")
	}
	return p, true, nil
}
