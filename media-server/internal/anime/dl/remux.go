package dl

import (
	"bufio"
	"errors"
	"io"
	"os"

	mp4 "github.com/yapingcat/gomedia/go-mp4"
	mpeg2 "github.com/yapingcat/gomedia/go-mpeg2"
)

// 把 TS 分段按顺序拼成一个 mp4（只重新封装、不重新编码，盒子 CPU 吃得消）。
// 用纯 Go 的 gomedia（盒子上没有 ffmpeg）。10-26 在盒子上实测：40 段 33.7MB 拼接 18 秒，
// 出来的 mp4 浏览器能播、能拖进度条。

// trackClock 让一路（视频或音频）的时间戳保持连续：分段之间时间戳倒退或突然跳很远
// （去掉广告段后的接缝、33 位时间戳回绕）时，把后面的整体平移接到前面后头。单位毫秒。
type trackClock struct {
	started bool
	last    int64 // 上一帧平移后的 dts
	step    int64 // 上一帧和再上一帧的间隔，接缝处按这个间隔接上
	offset  int64
}

const maxGapMs = 10_000

func (c *trackClock) fix(pts, dts uint64) (uint64, uint64) {
	p, d := int64(pts)+c.offset, int64(dts)+c.offset
	if c.started {
		if gap := d - c.last; gap < 0 || gap > maxGapMs {
			step := c.step
			if step <= 0 || step > 1000 {
				step = 40
			}
			shift := c.last + step - d
			c.offset += shift
			p, d = p+shift, d+shift
		} else if gap > 0 {
			c.step = gap
		}
	}
	c.started, c.last = true, d
	if p < d {
		p = d
	}
	return uint64(p), uint64(d)
}

// bufWriteSeeker 带缓冲的写文件：mp4 封装库按帧写（音频一帧才几百字节），还每帧问一次"写到哪了"，
// 直接写 *os.File 就是几十万次系统调用压在移动硬盘上（盒子上拼一集要好几分钟，CPU 闲着、全在等硬盘）。
// 问位置直接回答记下的数，真要跳位置（写文件尾时回头改 mdat 大小）才先把缓冲写下去
type bufWriteSeeker struct {
	f   *os.File
	w   *bufio.Writer
	pos int64
}

func (b *bufWriteSeeker) Write(p []byte) (int, error) {
	n, err := b.w.Write(p)
	b.pos += int64(n)
	return n, err
}

func (b *bufWriteSeeker) Seek(off int64, whence int) (int64, error) {
	if whence == io.SeekCurrent && off == 0 {
		return b.pos, nil
	}
	if err := b.w.Flush(); err != nil {
		return 0, err
	}
	p, err := b.f.Seek(off, whence)
	b.pos = p
	return p, err
}

// remuxTS 把 paths 里的 TS 分段（按顺序）拼成 out（mp4）
func remuxTS(paths []string, out string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := &bufWriteSeeker{f: f, w: bufio.NewWriterSize(f, 1<<20)}
	muxer, err := mp4.CreateMp4Muxer(w)
	if err != nil {
		return err
	}
	var (
		vt, at     uint32
		hasV, hasA bool
		vc, ac     trackClock
		writeErr   error
	)
	write := func(track uint32, clk *trackClock, frame []byte, pts, dts uint64) {
		if writeErr != nil {
			return
		}
		p, d := clk.fix(pts, dts)
		writeErr = muxer.Write(track, frame, p, d)
	}
	dm := mpeg2.NewTSDemuxer()
	dm.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts, dts uint64) {
		switch cid {
		case mpeg2.TS_STREAM_H264, mpeg2.TS_STREAM_H265:
			if !hasV {
				codec := mp4.MP4_CODEC_H264
				if cid == mpeg2.TS_STREAM_H265 {
					codec = mp4.MP4_CODEC_H265
				}
				vt, hasV = muxer.AddVideoTrack(codec), true
			}
			write(vt, &vc, frame, pts, dts)
		case mpeg2.TS_STREAM_AAC, mpeg2.TS_STREAM_AUDIO_MPEG1, mpeg2.TS_STREAM_AUDIO_MPEG2:
			if !hasA {
				codec := mp4.MP4_CODEC_AAC
				if cid != mpeg2.TS_STREAM_AAC {
					codec = mp4.MP4_CODEC_MP3
				}
				at, hasA = muxer.AddAudioTrack(codec), true
			}
			write(at, &ac, frame, pts, dts)
		}
	}
	r := &filesReader{paths: paths}
	defer r.Close()
	// 解析库一次只读 188 字节（一个 TS 包），不加缓冲一集就是两百多万次读
	if err := dm.Input(bufio.NewReaderSize(r, 1<<20)); err != nil {
		return errors.New("视频分段解析失败：" + err.Error())
	}
	if writeErr != nil {
		return errors.New("拼接视频失败：" + writeErr.Error())
	}
	if !hasV {
		return errors.New("没认出视频画面（可能是盒子不支持的视频格式）")
	}
	if err := muxer.WriteTrailer(); err != nil {
		return errors.New("拼接视频失败：" + err.Error())
	}
	if err := w.w.Flush(); err != nil {
		return errors.New("拼接视频失败：" + err.Error())
	}
	return f.Sync()
}
