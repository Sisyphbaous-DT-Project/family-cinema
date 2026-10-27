package dl

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// decryptAES128 按 HLS 规范解 AES-128-CBC（PKCS7 填充）。iv 为空时用分段序号（大端写在后 8 字节）。
func decryptAES128(data, key, iv []byte, seq int64) ([]byte, error) {
	if len(key) != 16 {
		return nil, errors.New("密钥长度不对")
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("加密分段长度不对")
	}
	if len(iv) != 16 {
		iv = make([]byte, 16)
		binary.BigEndian.PutUint64(iv[8:], uint64(seq))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(out) {
		return nil, errors.New("解密失败（密钥可能不对）")
	}
	return out[:len(out)-pad], nil
}

// stripToTS 有的站把 TS 分段伪装成图片（前面塞一段 PNG/JPG 头），找到第一个连续三个 TS 包同步字节的位置，
// 把前面的伪装头去掉。本来就以 0x47 开头的、或者根本找不到 TS 包的（比如 fMP4 分段），原样返回。
func stripToTS(b []byte) []byte {
	const pkt = 188
	if len(b) >= pkt && b[0] == 0x47 {
		return b
	}
	limit := len(b) - 2*pkt
	if limit > 1<<20 {
		limit = 1 << 20
	}
	for i := 0; i < limit; i++ {
		if b[i] == 0x47 && b[i+pkt] == 0x47 && b[i+2*pkt] == 0x47 {
			return b[i:]
		}
	}
	return b
}

// filesReader 把多个文件按顺序串成一个读流（一次只开一个文件，几百个分段也不会占满文件句柄）
type filesReader struct {
	paths []string
	cur   *os.File
}

func (r *filesReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			if len(r.paths) == 0 {
				return 0, io.EOF
			}
			f, err := os.Open(r.paths[0])
			if err != nil {
				return 0, err
			}
			r.cur, r.paths = f, r.paths[1:]
		}
		n, err := r.cur.Read(p)
		if err == io.EOF {
			r.cur.Close()
			r.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (r *filesReader) Close() {
	if r.cur != nil {
		r.cur.Close()
		r.cur = nil
	}
}
