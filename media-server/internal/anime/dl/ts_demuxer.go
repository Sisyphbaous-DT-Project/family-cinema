package dl

// 本文件基于 gomedia 的 TS 解复用器（MIT）优化：小包缓冲复用，视频帧增量扫描。
// 上游：https://github.com/yapingcat/gomedia （go-mpeg2/ts-demuxer.go）。

import (
	"errors"
	"io"

	"github.com/yapingcat/gomedia/go-codec"
	mpeg2 "github.com/yapingcat/gomedia/go-mpeg2"
)

const tsPacketSize = 188

type tsFrame struct {
	payload []byte
	pts     uint64
	dts     uint64
}

type tsStream struct {
	cid mpeg2.TS_STREAM_TYPE
	pes *mpeg2.PesPacket
	pkg *tsFrame

	nextStart     int
	searchFrom    int
	startCode     codec.START_CODE_TYPE
	frameBeg      int
	hasFrame      bool
	vcl           int
	newAccessUnit bool
}

type tsProgram struct {
	pn      uint16
	streams map[uint16]*tsStream
}

type tsDemuxer struct {
	programs map[uint16]*tsProgram
	OnFrame  func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts uint64, dts uint64)
}

func newTSDemuxer() *tsDemuxer {
	return &tsDemuxer{programs: make(map[uint16]*tsProgram)}
}

func (d *tsDemuxer) Input(r io.Reader) error {
	var readErr error
	var buf []byte
	var storage [2 * tsPacketSize]byte
	for {
		if len(buf) > tsPacketSize {
			buf = buf[tsPacketSize:]
		} else {
			if readErr != nil {
				if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
					break
				}
				return readErr
			}
			buf, readErr = readTSPackets(r, storage[:])
			if readErr != nil && buf == nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return readErr
			}
		}

		bs := codec.NewBitStream(buf[:tsPacketSize])
		var pkg mpeg2.TSPacket
		if err := pkg.DecodeHeader(bs); err != nil {
			return err
		}
		if pkg.PID == uint16(mpeg2.TS_PID_PAT) {
			if pkg.Payload_unit_start_indicator == 1 {
				bs.SkipBits(8)
			}
			payload, err := mpeg2.ReadSection(mpeg2.TS_TID_PAS, bs)
			if err != nil {
				return err
			}
			for _, pmt := range payload.(*mpeg2.Pat).Pmts {
				if pmt.Program_number != 0 {
					if _, found := d.programs[pmt.PID]; !found {
						d.programs[pmt.PID] = &tsProgram{streams: make(map[uint16]*tsStream)}
					}
				}
			}
			continue
		}
		if pkg.PID == mpeg2.TS_PID_Nil {
			continue
		}
		for p, program := range d.programs {
			if p == pkg.PID {
				if pkg.Payload_unit_start_indicator == 1 {
					bs.SkipBits(8)
				}
				payload, err := mpeg2.ReadSection(mpeg2.TS_TID_PMS, bs)
				if err != nil {
					return err
				}
				pmt := payload.(*mpeg2.Pmt)
				program.pn = pmt.Program_number
				for _, ps := range pmt.Streams {
					if _, found := program.streams[ps.Elementary_PID]; !found {
						program.streams[ps.Elementary_PID] = &tsStream{
							cid: mpeg2.TS_STREAM_TYPE(ps.StreamType),
							pes: mpeg2.NewPesPacket(), nextStart: -1,
						}
					}
				}
				continue
			}
			for sid, stream := range program.streams {
				if sid != pkg.PID {
					continue
				}
				if pkg.Payload_unit_start_indicator == 1 {
					err := stream.pes.Decode(bs)
					if err != nil && !(pesNeedsMore(err) && stream.pes.Pes_payload != nil) {
						return err
					}
				} else {
					stream.pes.Pes_payload = bs.RemainData()
				}
				switch stream.cid {
				case mpeg2.TS_STREAM_AAC, mpeg2.TS_STREAM_AUDIO_MPEG1, mpeg2.TS_STREAM_AUDIO_MPEG2:
					d.doAudioPesPacket(stream, pkg.Payload_unit_start_indicator)
				case mpeg2.TS_STREAM_H264, mpeg2.TS_STREAM_H265:
					d.doVideoPesPacket(stream)
				}
			}
		}
	}
	d.flush()
	return nil
}

func pesNeedsMore(err error) bool {
	var e mpeg2.Error
	return errors.As(err, &e) && e.NeedMore()
}

// readTSPackets 复用调用方给的缓冲，不再为每个 188 字节小包分配内存。
func readTSPackets(r io.Reader, buf []byte) ([]byte, error) {
	buf = buf[:cap(buf)]
	if _, err := io.ReadFull(r, buf[:tsPacketSize]); err != nil {
		return nil, err
	}
	if buf[0] == 0x47 {
		return buf[:tsPacketSize], nil
	}
	if _, err := io.ReadFull(r, buf[tsPacketSize:]); err != nil {
		return nil, err
	}
	for {
		i := 0
		for ; i < tsPacketSize; i++ {
			if buf[i] == 0x47 && buf[i+tsPacketSize] == 0x47 {
				break
			}
		}
		if i == 0 {
			return buf, nil
		}
		if i < tsPacketSize {
			copy(buf, buf[i:])
			if _, err := io.ReadFull(r, buf[2*tsPacketSize-i:]); err != nil {
				return buf[:tsPacketSize], err
			}
			return buf, nil
		}
		copy(buf, buf[tsPacketSize:])
		if _, err := io.ReadFull(r, buf[tsPacketSize:]); err != nil {
			return buf[:tsPacketSize], err
		}
	}
}

func (d *tsDemuxer) flush() {
	var streams []*tsStream
	for _, program := range d.programs {
		for _, stream := range program.streams {
			streams = append(streams, stream)
		}
	}
	// 收尾固定先视频后音频；上游直接遍历 map，同一份输入也可能给出不同顺序。
	for i := 1; i < len(streams); i++ {
		for j := i; j > 0 && streams[j].cid > streams[j-1].cid; j-- {
			streams[j], streams[j-1] = streams[j-1], streams[j]
		}
	}
	for _, stream := range streams {
		if stream.pkg == nil || len(stream.pkg.payload) == 0 || d.OnFrame == nil {
			continue
		}
		if stream.cid == mpeg2.TS_STREAM_H264 || stream.cid == mpeg2.TS_STREAM_H265 {
			audLen := 0
			codec.SplitFrameWithStartCode(stream.pkg.payload, func(nalu []byte) bool {
				if stream.cid == mpeg2.TS_STREAM_H264 {
					if codec.H264NaluType(nalu) == codec.H264_NAL_AUD {
						audLen += len(nalu)
					}
				} else if codec.H265NaluType(nalu) == codec.H265_NAL_AUD {
					audLen += len(nalu)
				}
				return false
			})
			d.OnFrame(stream.cid, stream.pkg.payload[audLen:], stream.pkg.pts/90, stream.pkg.dts/90)
		} else {
			d.OnFrame(stream.cid, stream.pkg.payload, stream.pkg.pts/90, stream.pkg.dts/90)
		}
		stream.pkg = nil
	}
}

func (d *tsDemuxer) doVideoPesPacket(stream *tsStream) {
	if stream.pkg == nil {
		stream.pkg = &tsFrame{payload: make([]byte, 0, 64<<10)}
		stream.pkg.pts, stream.pkg.dts = stream.pes.Pts, stream.pes.Dts
	}
	stream.pkg.payload = append(stream.pkg.payload, stream.pes.Pes_payload...)
	updated := false
	if stream.cid == mpeg2.TS_STREAM_H264 {
		updated = d.splitH264Frame(stream)
	} else {
		updated = d.splitH265Frame(stream)
	}
	if updated {
		stream.pkg.pts, stream.pkg.dts = stream.pes.Pts, stream.pes.Dts
	}
}

func (d *tsDemuxer) doAudioPesPacket(stream *tsStream, start uint8) {
	if stream.pkg == nil {
		stream.pkg = &tsFrame{payload: make([]byte, 0, 4<<10)}
		stream.pkg.pts, stream.pkg.dts = stream.pes.Pts, stream.pes.Dts
	}
	if len(stream.pkg.payload) > 0 && (start == 1 || stream.pes.Pts != stream.pkg.pts) {
		if d.OnFrame != nil {
			d.OnFrame(stream.cid, stream.pkg.payload, stream.pkg.pts/90, stream.pkg.dts/90)
		}
		stream.pkg.payload = stream.pkg.payload[:0]
	}
	stream.pkg.payload = append(stream.pkg.payload, stream.pes.Pes_payload...)
	stream.pkg.pts, stream.pkg.dts = stream.pes.Pts, stream.pes.Dts
}

func (d *tsDemuxer) nextNALU(stream *tsStream, data []byte, start int) (int, codec.START_CODE_TYPE) {
	end, sct := codec.FindStartCode(data, start+3)
	if end < 0 {
		stream.nextStart = -1
		stream.searchFrom = max(start+3, len(data)-2)
		return -1, sct
	}
	return end, sct
}

func (d *tsDemuxer) compactFrame(stream *tsStream, data []byte) {
	if stream.frameBeg == 0 {
		return
	}
	copy(stream.pkg.payload, data[stream.frameBeg:])
	stream.pkg.payload = stream.pkg.payload[:len(data)-stream.frameBeg]
	if stream.nextStart >= 0 {
		stream.nextStart -= stream.frameBeg
	}
	stream.searchFrom = max(0, stream.searchFrom-stream.frameBeg)
	stream.frameBeg = 0
}

func (d *tsDemuxer) splitH264Frame(stream *tsStream) bool {
	data := stream.pkg.payload
	start, sct := stream.nextStart, stream.startCode
	if start < 0 {
		start, sct = codec.FindStartCode(data, stream.searchFrom)
		if start < 0 {
			stream.searchFrom = max(stream.searchFrom, len(data)-2)
		}
	}
	if start >= 0 && !stream.hasFrame {
		stream.frameBeg, stream.hasFrame = start, true
	}
	needUpdate := false
	for start >= 0 && start < len(data) {
		if len(data)-start <= int(sct)+1 {
			break
		}
		naluType := codec.H264NaluTypeWithoutStartCode(data[start+int(sct):])
		switch naluType {
		case codec.H264_NAL_AUD, codec.H264_NAL_SPS, codec.H264_NAL_PPS, codec.H264_NAL_SEI:
			if stream.vcl > 0 {
				stream.newAccessUnit = true
			}
		case codec.H264_NAL_I_SLICE, codec.H264_NAL_P_SLICE, codec.H264_NAL_SLICE_A,
			codec.H264_NAL_SLICE_B, codec.H264_NAL_SLICE_C:
			if stream.vcl > 0 {
				if data[start+int(sct)+1]&0x80 > 0 {
					stream.newAccessUnit = true
				}
			} else {
				stream.vcl++
			}
		}
		if stream.vcl > 0 && stream.newAccessUnit {
			if d.OnFrame != nil {
				audLen := 0
				codec.SplitFrameWithStartCode(data[stream.frameBeg:start], func(nalu []byte) bool {
					if codec.H264NaluType(nalu) == codec.H264_NAL_AUD {
						audLen += len(nalu)
					}
					return false
				})
				d.OnFrame(stream.cid, data[stream.frameBeg+audLen:start], stream.pkg.pts/90, stream.pkg.dts/90)
			}
			stream.frameBeg = start
			needUpdate = true
			stream.vcl, stream.newAccessUnit = 0, false
		}
		start, sct = d.nextNALU(stream, data, start)
	}
	stream.nextStart, stream.startCode = start, sct
	d.compactFrame(stream, data)
	return needUpdate
}

func (d *tsDemuxer) splitH265Frame(stream *tsStream) bool {
	data := stream.pkg.payload
	start, sct := stream.nextStart, stream.startCode
	if start < 0 {
		start, sct = codec.FindStartCode(data, stream.searchFrom)
		if start < 0 {
			stream.searchFrom = max(stream.searchFrom, len(data)-2)
		}
	}
	if start >= 0 && !stream.hasFrame {
		stream.frameBeg, stream.hasFrame = start, true
	}
	needUpdate := false
	for start >= 0 && start < len(data) {
		if len(data)-start <= int(sct)+2 {
			break
		}
		naluType := codec.H265NaluTypeWithoutStartCode(data[start+int(sct):])
		switch naluType {
		case codec.H265_NAL_AUD, codec.H265_NAL_SPS, codec.H265_NAL_PPS, codec.H265_NAL_VPS, codec.H265_NAL_SEI:
			if stream.vcl > 0 {
				stream.newAccessUnit = true
			}
		case codec.H265_NAL_Slice_TRAIL_N, codec.H265_NAL_LICE_TRAIL_R,
			codec.H265_NAL_SLICE_TSA_N, codec.H265_NAL_SLICE_TSA_R,
			codec.H265_NAL_SLICE_STSA_N, codec.H265_NAL_SLICE_STSA_R,
			codec.H265_NAL_SLICE_RADL_N, codec.H265_NAL_SLICE_RADL_R,
			codec.H265_NAL_SLICE_RASL_N, codec.H265_NAL_SLICE_RASL_R,
			codec.H265_NAL_SLICE_BLA_W_LP, codec.H265_NAL_SLICE_BLA_W_RADL,
			codec.H265_NAL_SLICE_BLA_N_LP, codec.H265_NAL_SLICE_IDR_W_RADL,
			codec.H265_NAL_SLICE_IDR_N_LP, codec.H265_NAL_SLICE_CRA:
			if stream.vcl > 0 {
				if data[start+int(sct)+2]&0x80 > 0 {
					stream.newAccessUnit = true
				}
			} else {
				stream.vcl++
			}
		}
		if stream.vcl > 0 && stream.newAccessUnit {
			if d.OnFrame != nil {
				audLen := 0
				codec.SplitFrameWithStartCode(data[stream.frameBeg:start], func(nalu []byte) bool {
					if codec.H265NaluType(nalu) == codec.H265_NAL_AUD {
						audLen = len(nalu)
					}
					return false
				})
				d.OnFrame(stream.cid, data[stream.frameBeg+audLen:start], stream.pkg.pts/90, stream.pkg.dts/90)
			}
			stream.frameBeg = start
			needUpdate = true
			stream.vcl, stream.newAccessUnit = 0, false
		}
		start, sct = d.nextNALU(stream, data, start)
	}
	stream.nextStart, stream.startCode = start, sct
	d.compactFrame(stream, data)
	return needUpdate
}
