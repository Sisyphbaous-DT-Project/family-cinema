package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type mediaProbe struct {
	Streams []mediaProbeStream `json:"streams"`
	Format  mediaProbeFormat   `json:"format"`
}

type mediaProbeStream struct {
	Index       int               `json:"index"`
	CodecType   string            `json:"codec_type"`
	CodecName   string            `json:"codec_name"`
	Tags        map[string]string `json:"tags"`
	Disposition mediaDisposition  `json:"disposition"`
}

type mediaDisposition struct {
	Default     int `json:"default"`
	Forced      int `json:"forced"`
	AttachedPic int `json:"attached_pic"`
}

type mediaProbeFormat struct {
	Duration string `json:"duration"`
}

type mediaProbeCache struct {
	Size    int64      `json:"size"`
	ModTime int64      `json:"modTime"`
	Probe   mediaProbe `json:"probe"`
}

type mediaAudioPlan struct {
	StreamIndex int
	Copy        bool
}

type mediaSubtitlePlan struct {
	StreamIndex int
	Kind        string
	Lang        string
	Title       string
	Forced      bool
	SourcePath  string
	FinalPath   string
	TempPath    string
	External    bool
	// InputTemp 是外挂字幕转成 UTF-8 后的临时副本；原文件本来就是 UTF-8 时为空。
	InputTemp string
}

// mediaKeepStream 是要原样另存的一条流。
type mediaKeepStream struct {
	Index int
	Type  string
	Codec string
}

// mediaKeepPlan 把网页放不了、但删原片前必须保住的字幕（以及 ASS 用的字体）原样另存成 .mks。
type mediaKeepPlan struct {
	Streams   []mediaKeepStream
	Subtitles int
	// Unsupported 是其中网页放不了的字幕条数；Fallback 表示是内嵌字幕转不出来才整体另存的
	Unsupported int
	Fallback    bool
	FinalPath   string
	TempPath    string
}

// note 是任务完成后写给人看的一句话。
func (k *mediaKeepPlan) note() string {
	name := filepath.Base(k.FinalPath)
	switch {
	case k.Fallback:
		return "内嵌字幕没能转成网页能放的格式，已原样另存为 " + name + "，电脑播放器能看"
	case k.Unsupported != 0:
		return fmt.Sprintf("有 %d 条字幕网页放不了，已原样另存为 %s，电脑播放器能看", k.Unsupported, name)
	default:
		return "ASS 字幕连同字体另存了一份 " + name
	}
}

type mediaPlan struct {
	SourcePath   string
	Probe        mediaProbe
	Duration     float64
	VideoCodec   string
	ConvertAudio bool
	Audio        []mediaAudioPlan
	Subtitles    []mediaSubtitlePlan
	Keep         *mediaKeepPlan
	VideoTarget  string
	VideoTemp    string
}

func (p *mediaPlan) tempFiles() []string {
	var out []string
	if p.VideoTemp != "" {
		out = append(out, p.VideoTemp)
	}
	for _, sub := range p.Subtitles {
		out = append(out, sub.TempPath)
		if sub.External {
			// 编码转换的副本可能建也可能不建，先按名字登记上，断电后恢复时一并清掉
			out = append(out, sub.inputTempPath())
		}
	}
	if p.Keep != nil {
		out = append(out, p.Keep.TempPath)
	}
	return out
}

func (s mediaSubtitlePlan) inputTempPath() string {
	return s.FinalPath + ".src.fcwork"
}

func (p *mediaPlan) hasWork() bool {
	return p.ConvertAudio || len(p.Subtitles) != 0
}

func (p *mediaPlan) hasInternalSubtitles() bool {
	for _, sub := range p.Subtitles {
		if !sub.External {
			return true
		}
	}
	return false
}

// keepAllSubtitles 是内嵌字幕转换失败时的退路：所有内嵌字幕都改为原样另存进 .mks，
// 不再单独抽 .sup/.vtt/.ass，这样声音照样能修好，字幕数据也一条不丢。
func (p *mediaPlan) keepAllSubtitles() bool {
	if !p.ConvertAudio {
		return false
	}
	var subs []mediaSubtitlePlan
	internal := false
	for _, sub := range p.Subtitles {
		if sub.External {
			subs = append(subs, sub)
		} else {
			internal = true
		}
	}
	if !internal {
		return false
	}
	p.Subtitles = subs
	keep := p.Keep
	if keep == nil {
		keep = &mediaKeepPlan{}
		keep.FinalPath = availableMediaKeepPath(strings.TrimSuffix(p.SourcePath, filepath.Ext(p.SourcePath)), nil)
		keep.TempPath = keep.FinalPath + ".fcwork"
	}
	keep.Streams = nil
	keep.Subtitles = 0
	keep.Fallback = true
	for _, stream := range p.Probe.Streams {
		switch stream.CodecType {
		case "subtitle":
			keep.Streams = append(keep.Streams, mediaKeepStream{Index: stream.Index, Type: "subtitle", Codec: strings.ToLower(stream.CodecName)})
			keep.Subtitles++
		case "attachment":
			keep.Streams = append(keep.Streams, mediaKeepStream{Index: stream.Index, Type: "attachment", Codec: strings.ToLower(stream.CodecName)})
		}
	}
	p.Keep = keep
	return true
}

func buildMediaPlan(path string, probe mediaProbe) (mediaPlan, error) {
	plan := mediaPlan{SourcePath: path, Probe: probe}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || duration <= 0 {
		return plan, fmt.Errorf("读不到视频时长")
	}
	plan.Duration = duration

	video := -1
	videoSupported := false
	incompatibleAudio := false
	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			if video == -1 && stream.Disposition.AttachedPic == 0 {
				video = stream.Index
				plan.VideoCodec = strings.ToLower(stream.CodecName)
				videoSupported = map[string]bool{"h264": true, "hevc": true, "av1": true, "vp9": true}[plan.VideoCodec]
			}
		case "audio":
			codec := strings.ToLower(stream.CodecName)
			copyAudio := codec == "aac" || codec == "mp3"
			plan.Audio = append(plan.Audio, mediaAudioPlan{StreamIndex: stream.Index, Copy: copyAudio})
			if !map[string]bool{"aac": true, "mp3": true, "opus": true, "flac": true, "vorbis": true}[codec] {
				incompatibleAudio = true
			}
		}
	}
	if video == -1 {
		return plan, fmt.Errorf("没有可识别的视频流")
	}
	plan.ConvertAudio = videoSupported && incompatibleAudio && len(plan.Audio) != 0
	if plan.ConvertAudio {
		base := strings.TrimSuffix(path, filepath.Ext(path))
		plan.VideoTarget = base + ".mp4"
		plan.VideoTemp = plan.VideoTarget + ".fcwork"
	}

	base := strings.TrimSuffix(path, filepath.Ext(path))
	used := map[string]bool{}
	if plan.VideoTemp != "" {
		used[plan.VideoTemp] = true
	}
	var keep, styled, fonts []mediaKeepStream
	for _, stream := range probe.Streams {
		if stream.CodecType == "attachment" {
			fonts = append(fonts, mediaKeepStream{Index: stream.Index, Type: "attachment", Codec: strings.ToLower(stream.CodecName)})
			continue
		}
		if stream.CodecType != "subtitle" {
			continue
		}
		codec := strings.ToLower(stream.CodecName)
		if codec == "ass" || codec == "ssa" {
			styled = append(styled, mediaKeepStream{Index: stream.Index, Type: "subtitle", Codec: codec})
		}
		lang := mediaSubtitleLanguage(stream.Tags["language"])
		newOutput := func(kind, ext string) {
			canonical := fmt.Sprintf("%s.s%d.%s.%s", base, stream.Index, lang, ext)
			if info, err := os.Stat(canonical); err == nil && info.Size() > 0 {
				return
			}
			final := availableMediaSubtitlePath(base, stream.Index, lang, ext, used)
			used[final] = true
			used[final+".fcwork"] = true
			plan.Subtitles = append(plan.Subtitles, mediaSubtitlePlan{
				StreamIndex: stream.Index, Kind: kind, Lang: lang,
				Title: strings.TrimSpace(stream.Tags["title"]), Forced: stream.Disposition.Forced != 0,
				FinalPath: final, TempPath: final + ".fcwork",
			})
		}
		switch codec {
		case "hdmv_pgs_subtitle":
			newOutput("pgs", "sup")
		case "subrip", "ass", "ssa", "mov_text", "webvtt", "text":
			newOutput("text", "vtt")
			if codec == "ass" || codec == "ssa" {
				newOutput("text", "ass")
			}
		default:
			// 网页放不了的格式（如老 DVD 的图片字幕）：只抽字幕时原片不动，转声音时另存进 .mks。
			keep = append(keep, mediaKeepStream{Index: stream.Index, Type: "subtitle", Codec: codec})
		}
	}
	// 转声音后原片会被删掉：网页放不了的字幕要原样另存；ASS 带的字体也一起存，免得别的播放器显示走样。
	if plan.ConvertAudio && (len(keep) != 0 || (len(styled) != 0 && len(fonts) != 0)) {
		streams := keep
		if len(styled) != 0 && len(fonts) != 0 {
			streams = append(append(streams, styled...), fonts...)
		}
		final := availableMediaKeepPath(base, used)
		used[final] = true
		plan.Keep = &mediaKeepPlan{Streams: streams, Unsupported: len(keep), FinalPath: final, TempPath: final + ".fcwork"}
		for _, stream := range streams {
			if stream.Type == "subtitle" {
				plan.Keep.Subtitles++
			}
		}
	}
	return plan, nil
}

// availableMediaKeepPath 给另存字幕的 .mks 找一个不会覆盖已有文件的名字。
func availableMediaKeepPath(base string, used map[string]bool) string {
	for n := 1; ; n++ {
		candidate := base + ".subs.mks"
		if n > 1 {
			candidate = fmt.Sprintf("%s.subs%d.mks", base, n)
		}
		if used[candidate] {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			continue
		}
		return candidate
	}
}

func availableMediaSubtitlePath(base string, index int, lang, ext string, used map[string]bool) string {
	for n := index; ; n++ {
		candidate := fmt.Sprintf("%s.s%d.%s.%s", base, n, lang, ext)
		if used[candidate] {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			continue
		}
		return candidate
	}
}

func mediaSubtitleLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return "und"
	}
	var b strings.Builder
	for _, r := range lang {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "und"
	}
	return b.String()
}

func buildMediaFFmpegArgs(plan mediaPlan) []string {
	args := []string{"-hide_banner", "-nostdin", "-y", "-v", "error", "-progress", "pipe:1", "-nostats"}
	if plan.needsDownmix() {
		args = append(args, "-downmix", "stereo")
	}
	args = append(args, "-i", plan.SourcePath)
	if plan.ConvertAudio {
		args = append(args, "-map", "0:V:0", "-c:v", "copy")
		if plan.VideoCodec == "hevc" {
			args = append(args, "-tag:v", "hvc1")
		}
		for i, audio := range plan.Audio {
			args = append(args, "-map", fmt.Sprintf("0:%d", audio.StreamIndex))
			if audio.Copy {
				args = append(args, fmt.Sprintf("-c:a:%d", i), "copy")
			} else {
				args = append(args, fmt.Sprintf("-c:a:%d", i), "aac", fmt.Sprintf("-b:a:%d", i), "192k", fmt.Sprintf("-ac:a:%d", i), "2")
			}
			disposition := "0"
			if i == 0 {
				disposition = "default"
			}
			args = append(args, fmt.Sprintf("-disposition:a:%d", i), disposition)
		}
		args = append(args, "-aac_coder", "fast", "-max_muxing_queue_size", "4096",
			"-map_metadata", "0", "-map_chapters", "0", "-movflags", "+faststart", "-f", "mp4", plan.VideoTemp)
	}
	for _, sub := range plan.Subtitles {
		if sub.External {
			continue
		}
		args = append(args, "-map", fmt.Sprintf("0:%d", sub.StreamIndex))
		switch strings.ToLower(filepath.Ext(sub.FinalPath)) {
		case ".sup":
			args = append(args, "-c:s", "copy", "-f", "sup")
		case ".ass":
			args = append(args, "-c:s", "copy", "-f", "ass")
		default:
			args = append(args, "-c:s", "webvtt", "-f", "webvtt")
		}
		args = append(args, sub.TempPath)
	}
	if plan.Keep != nil && len(plan.Keep.Streams) != 0 {
		for _, stream := range plan.Keep.Streams {
			args = append(args, "-map", fmt.Sprintf("0:%d", stream.Index))
		}
		args = append(args, "-c", "copy")
		// MKV 装不下 MP4 自带的文字字幕，换成 SRT 存，内容不变。
		n := 0
		for _, stream := range plan.Keep.Streams {
			if stream.Type != "subtitle" {
				continue
			}
			if stream.Codec == "mov_text" || stream.Codec == "text" {
				args = append(args, fmt.Sprintf("-c:s:%d", n), "srt")
			}
			n++
		}
		args = append(args, "-max_muxing_queue_size", "4096", "-f", "matroska", plan.Keep.TempPath)
	}
	return args
}

func (p *mediaPlan) needsDownmix() bool {
	if !p.ConvertAudio {
		return false
	}
	for _, stream := range p.Probe.Streams {
		if stream.CodecType != "audio" {
			continue
		}
		codec := strings.ToLower(stream.CodecName)
		if codec == "ac3" || codec == "eac3" {
			return true
		}
	}
	return false
}

func mediaFirstAudioLanguage(probe *mediaProbe) string {
	if probe == nil {
		return ""
	}
	for _, stream := range probe.Streams {
		if stream.CodecType == "audio" {
			return strings.TrimSpace(stream.Tags["language"])
		}
	}
	return ""
}
