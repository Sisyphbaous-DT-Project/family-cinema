package dl

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"reflect"
	"testing"

	mpeg2 "github.com/yapingcat/gomedia/go-mpeg2"
)

type demuxedFrame struct {
	pts, dts uint64
	size     int
	hash     [32]byte
}

func makeTS(t *testing.T, cid mpeg2.TS_STREAM_TYPE) []byte {
	t.Helper()
	var data bytes.Buffer
	mux := mpeg2.NewTSMuxer()
	mux.OnPacket = func(pkg []byte) { data.Write(pkg) }
	video := mux.AddStream(cid)
	audio := mux.AddStream(mpeg2.TS_STREAM_AAC)
	for i := 0; i < 5; i++ {
		var frame []byte
		if cid == mpeg2.TS_STREAM_H264 {
			frame = []byte{0, 0, 0, 1, 0x09, 0xf0, 0, 0, 0, 1,
				0x67, 0x64, 0, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01, 0x10,
				0, 0, 3, 0, 0x10, 0, 0, 3, 0x03, 0x20, 0xf1, 0x83, 0x19, 0x60,
				0, 0, 0, 1, 0x68, 0xeb, 0xe3, 0xcb, 0x22, 0xc0}
			if i > 0 {
				frame = []byte{0, 0, 0, 1, 0x09, 0xf0, 0, 0, 1, 0x41, 0x80}
			} else {
				frame = append(frame, 0, 0, 0, 1, 0x65, 0x80)
			}
		} else {
			frame = []byte{0, 0, 0, 1, 0x46, 0x01, 0x50, 0, 0, 0, 1, 0x40, 0x01, 0, 0, 0, 1, 0x42, 0x01, 0, 0, 0, 1, 0x44, 0x01}
			if i > 0 {
				frame = []byte{0, 0, 0, 1, 0x46, 0x01, 0x50, 0, 0, 1, 0x02, 0x01, 0x80}
			} else {
				frame = append(frame, 0, 0, 0, 1, 0x26, 0x01, 0x80)
			}
		}
		frame = append(frame, bytes.Repeat([]byte{byte(i + 1), 0x11, 0x22}, 40*1024)...)
		pts := uint64(i * 40)
		if err := mux.Write(video, frame, pts, pts); err != nil {
			t.Fatal(err)
		}
		aac := append([]byte{0xff, 0xf1, 0x50, 0x80, 0x01, 0x1f, 0xfc}, bytes.Repeat([]byte{byte(i + 3)}, 256)...)
		if err := mux.Write(audio, aac, pts, pts); err != nil {
			t.Fatal(err)
		}
	}
	return data.Bytes()
}

func demuxFrames(t *testing.T, data []byte, chunk int, optimized bool) map[mpeg2.TS_STREAM_TYPE][]demuxedFrame {
	t.Helper()
	out := make(map[mpeg2.TS_STREAM_TYPE][]demuxedFrame)
	onFrame := func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts, dts uint64) {
		out[cid] = append(out[cid], demuxedFrame{pts: pts, dts: dts, size: len(frame), hash: sha256.Sum256(frame)})
	}
	var r io.Reader = bytes.NewReader(data)
	if chunk > 0 {
		r = &chunkReader{data: data, chunk: chunk}
	}
	if optimized {
		dm := newTSDemuxer()
		dm.OnFrame = onFrame
		if err := dm.Input(r); err != nil {
			t.Fatal(err)
		}
	} else {
		dm := mpeg2.NewTSDemuxer()
		dm.OnFrame = onFrame
		if err := dm.Input(r); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

type chunkReader struct {
	data  []byte
	chunk int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(len(r.data), r.chunk, len(p))
	n = copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestTSDemuxerMatchesUpstream(t *testing.T) {
	for _, cid := range []mpeg2.TS_STREAM_TYPE{mpeg2.TS_STREAM_H264, mpeg2.TS_STREAM_H265} {
		t.Run(fmt.Sprintf("codec-%d", cid), func(t *testing.T) {
			data := makeTS(t, cid)
			want := demuxFrames(t, data, 0, false)
			for _, chunk := range []int{1, 7, 188, 4096, 0} {
				got := demuxFrames(t, data, chunk, true)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("块大小 %d：帧内容或时间戳与上游不同\n旧：%+v\n新：%+v", chunk, want, got)
				}
			}
		})
	}
}

func TestMergeProgressReader(t *testing.T) {
	const total = 32
	var got []MergeProgress
	r := &mergeProgressReader{
		r: &chunkReader{data: bytes.Repeat([]byte{1}, total), chunk: 7}, total: total,
		report: func(p MergeProgress) { got = append(got, p) },
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("没有进度上报")
	}
	for i, p := range got {
		if p.Total != total || p.Done > total || (i > 0 && p.Done < got[i-1].Done) {
			t.Fatalf("进度不对: %+v", got)
		}
	}
	if last := got[len(got)-1]; last.Done != total || last.Finalizing {
		t.Fatalf("读取末尾进度不对: %+v", last)
	}
}
