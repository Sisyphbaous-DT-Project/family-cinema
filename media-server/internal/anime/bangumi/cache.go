// Package bangumi 封装 Bangumi 公开 API（番剧资料/时间表/搜索）并提供磁盘缓存与图片代理。
package bangumi

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type cacheEntry struct {
	Exp  int64           `json:"exp"` // 过期时间（UnixNano）
	Data json.RawMessage `json:"data"`
}

// Cache 缓存上游返回的原始 JSON：内存 + 磁盘，盒子重启后仍可用。
// 上游抓取失败时，如果有过期的旧数据就先用旧的（Bangumi 偶尔抽风，不该让首页变空）。
type Cache struct {
	dir string
	mu  sync.Mutex
	mem map[string]cacheEntry
	sf  singleflight.Group
}

// NewCache dir 为空则只用内存
func NewCache(dir string) *Cache {
	if dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return &Cache{dir: dir, mem: map[string]cacheEntry{}}
}

func (c *Cache) path(key string) string {
	sum := sha1.Sum([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".json")
}

func (c *Cache) load(key string) (cacheEntry, bool) {
	c.mu.Lock()
	e, ok := c.mem[key]
	c.mu.Unlock()
	if ok {
		return e, true
	}
	if c.dir == "" {
		return cacheEntry{}, false
	}
	raw, err := os.ReadFile(c.path(key))
	if err != nil || json.Unmarshal(raw, &e) != nil {
		return cacheEntry{}, false
	}
	c.mu.Lock()
	c.mem[key] = e
	c.mu.Unlock()
	return e, true
}

func (c *Cache) store(key string, e cacheEntry) {
	c.mu.Lock()
	c.mem[key] = e
	c.mu.Unlock()
	if c.dir == "" {
		return
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	// 先写临时文件再改名，避免断电/崩溃留下半截 JSON
	tmp := c.path(key) + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, c.path(key))
	}
}

// Get 取缓存；过期或不存在时调用 fetch 抓取并写回。并发的相同 key 只会抓一次。
func (c *Cache) Get(ctx context.Context, key string, ttl time.Duration, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	if e, ok := c.load(key); ok && time.Now().UnixNano() < e.Exp {
		return e.Data, nil
	}
	v, err, _ := c.sf.Do(key, func() (any, error) {
		// 抓取不受第一个调用者取消的影响，否则它一断开，等同一个 key 的其他请求会跟着失败
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		data, err := fetch(fctx)
		if err != nil {
			if e, ok := c.load(key); ok {
				return []byte(e.Data), nil // 兜底用过期数据
			}
			return nil, err
		}
		c.store(key, cacheEntry{Exp: time.Now().Add(ttl).UnixNano(), Data: data})
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}
