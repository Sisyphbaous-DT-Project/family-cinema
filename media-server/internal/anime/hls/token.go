// Package hls 实现播放代理：把第三方视频地址（m3u8/mp4）封进签名令牌，
// 由盒子带上 Referer 等请求头去拉，并重写 m3u8 让分片也走代理。
package hls

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Target 令牌里封装的上游资源及其请求头
type Target struct {
	URL     string `json:"u"`
	Referer string `json:"r,omitempty"`
	UA      string `json:"a,omitempty"`
	Cookie  string `json:"c,omitempty"`
	AdBlock bool   `json:"b,omitempty"` // 对 m3u8 做广告段过滤
	// Relay 是在线手机助手的代取地址；只有盒子直连失败时才由解析器填入
	Relay           string            `json:"l,omitempty"`
	RelayToken      string            `json:"t,omitempty"`
	Headers         map[string]string `json:"h,omitempty"` // 手机原视频请求里的安全播放器头（Origin 等）
	FallbackReferer string            `json:"f,omitempty"` // 原请求没有 Referer 时，仅在 404 后备用
}

// Signer 用进程启动时随机生成的密钥签名；重启后旧令牌失效，播放器会重新解析，可以接受。
type Signer struct{ key []byte }

func NewSigner() *Signer {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err) // 系统随机数不可用时服务不该继续跑
	}
	return &Signer{key: k}
}

func (s *Signer) mac(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Sign 生成 "<载荷>.<签名>" 形式的令牌（URL 安全）
func (s *Signer) Sign(t Target) string {
	raw, _ := json.Marshal(t)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + s.mac(payload)
}

var errBadToken = errors.New("令牌无效")

// Verify 校验签名并还原目标
func (s *Signer) Verify(tok string) (Target, error) {
	i := strings.LastIndexByte(tok, '.')
	if i <= 0 {
		return Target{}, errBadToken
	}
	payload, sig := tok[:i], tok[i+1:]
	if !hmac.Equal([]byte(sig), []byte(s.mac(payload))) {
		return Target{}, errBadToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Target{}, errBadToken
	}
	var t Target
	if json.Unmarshal(raw, &t) != nil || t.URL == "" {
		return Target{}, errBadToken
	}
	return t, nil
}
