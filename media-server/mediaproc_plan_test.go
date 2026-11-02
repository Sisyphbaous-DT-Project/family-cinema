package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mediaTestProbe(video string, audio []string, subtitles ...string) mediaProbe {
	probe := mediaProbe{Format: mediaProbeFormat{Duration: "100.000000"}}
	probe.Streams = append(probe.Streams, mediaProbeStream{Index: 0, CodecType: "video", CodecName: video})
	index := 1
	for _, codec := range audio {
		probe.Streams = append(probe.Streams, mediaProbeStream{Index: index, CodecType: "audio", CodecName: codec, Tags: map[string]string{"language": "eng"}})
		index++
	}
	for _, codec := range subtitles {
		probe.Streams = append(probe.Streams, mediaProbeStream{Index: index, CodecType: "subtitle", CodecName: codec, Tags: map[string]string{"language": "zho", "title": "中文字幕"}})
		index++
	}
	return probe
}

func TestMediaProcPlanDecisions(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name          string
		filename      string
		probe         mediaProbe
		convert       bool
		subtitles     int
		downmix       bool
		wantVideoName string
	}{
		{name: "HEVC 双 AC3 和五路 PGS 全量处理", filename: "film.mkv", probe: mediaTestProbe("hevc", []string{"ac3", "ac3"}, "hdmv_pgs_subtitle", "hdmv_pgs_subtitle", "hdmv_pgs_subtitle", "hdmv_pgs_subtitle", "hdmv_pgs_subtitle"), convert: true, subtitles: 5, downmix: true, wantVideoName: "film.mp4"},
		{name: "MKV AAC 只抽文字字幕", filename: "show.mkv", probe: mediaTestProbe("h264", []string{"aac"}, "subrip"), subtitles: 1},
		{name: "MP4 AAC 无字幕无操作", filename: "done.mp4", probe: mediaTestProbe("h264", []string{"aac"})},
		{name: "AVI mpeg4 不转声音", filename: "old.avi", probe: mediaTestProbe("mpeg4", []string{"mp3"})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := buildMediaPlan(filepath.Join(dir, test.filename), test.probe)
			if err != nil {
				t.Fatal(err)
			}
			if plan.ConvertAudio != test.convert || len(plan.Subtitles) != test.subtitles || plan.needsDownmix() != test.downmix {
				t.Fatalf("判定不对: convert=%v subtitles=%d downmix=%v", plan.ConvertAudio, len(plan.Subtitles), plan.needsDownmix())
			}
			if test.wantVideoName != "" && filepath.Base(plan.VideoTarget) != test.wantVideoName {
				t.Fatalf("视频输出名 = %s", plan.VideoTarget)
			}
			if !test.convert && plan.VideoTemp != "" {
				t.Fatal("只抽字幕不应生成新视频")
			}
		})
	}
}

func TestMediaProcPlanSkipsAttachedCoverAndKeepsASS(t *testing.T) {
	probe := mediaTestProbe("hevc", []string{"eac3", "aac"}, "ass")
	probe.Streams = append([]mediaProbeStream{{Index: 9, CodecType: "video", CodecName: "mjpeg", Disposition: mediaDisposition{AttachedPic: 1}}}, probe.Streams...)
	plan, err := buildMediaPlan(filepath.Join(t.TempDir(), "movie.mkv"), probe)
	if err != nil {
		t.Fatal(err)
	}
	if plan.VideoCodec != "hevc" || !plan.ConvertAudio || !plan.needsDownmix() {
		t.Fatalf("封面或 EAC3 判定不对: %+v", plan)
	}
	if len(plan.Subtitles) != 2 || filepath.Ext(plan.Subtitles[0].FinalPath) != ".vtt" || filepath.Ext(plan.Subtitles[1].FinalPath) != ".ass" {
		t.Fatalf("ASS 应同时保留 VTT 和 ASS: %+v", plan.Subtitles)
	}
	args := buildMediaFFmpegArgs(plan)
	if !mediaArgsContain(args, "-c:a:0", "aac") || !mediaArgsContain(args, "-c:a:1", "copy") {
		t.Fatalf("AAC/MP3 应直接复制，其它声音转 AAC: %v", args)
	}
}

func TestMediaProcFFmpegArguments(t *testing.T) {
	plan, err := buildMediaPlan(filepath.Join(t.TempDir(), "film.mkv"), mediaTestProbe("hevc", []string{"ac3", "aac"}, "hdmv_pgs_subtitle"))
	if err != nil {
		t.Fatal(err)
	}
	args := buildMediaFFmpegArgs(plan)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-downmix stereo -i", "-map 0:V:0 -c:v copy", "-tag:v hvc1",
		"-disposition:a:0 default", "-disposition:a:1 0", "-aac_coder fast",
		"-max_muxing_queue_size 4096", "-map_metadata 0 -map_chapters 0", "-movflags +faststart -f mp4",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("命令缺少 %q: %s", want, joined)
		}
	}
	videoOutput := -1
	for i, arg := range args {
		if arg == plan.VideoTemp {
			videoOutput = i
		}
	}
	if videoOutput < 0 || strings.Contains(strings.Join(args[:videoOutput], " "), fmt.Sprintf("0:%d", plan.Subtitles[0].StreamIndex)) {
		t.Fatal("PGS 不应 map 进 MP4")
	}
	if filepath.Base(plan.Subtitles[0].FinalPath) != "film.s3.zho.sup" || plan.Subtitles[0].TempPath != plan.Subtitles[0].FinalPath+".fcwork" {
		t.Fatalf("字幕输出名不对: %+v", plan.Subtitles[0])
	}

	h264, err := buildMediaPlan(filepath.Join(t.TempDir(), "h264.mkv"), mediaTestProbe("h264", []string{"aac"}, "subrip"))
	if err != nil {
		t.Fatal(err)
	}
	h264Args := strings.Join(buildMediaFFmpegArgs(h264), " ")
	if strings.Contains(h264Args, "hvc1") || strings.Contains(h264Args, "-downmix") {
		t.Fatalf("非 HEVC、无 AC3 不应出现这些参数: %s", h264Args)
	}

	avi, err := buildMediaPlan(filepath.Join(t.TempDir(), "old.avi"), mediaTestProbe("mpeg4", []string{"ac3"}, "subrip"))
	if err != nil {
		t.Fatal(err)
	}
	if avi.ConvertAudio || strings.Contains(strings.Join(buildMediaFFmpegArgs(avi), " "), "-downmix") {
		t.Fatal("不支持的视频编码不应触发声音转换或 downmix")
	}
}

func TestMediaProcExternalSubtitles(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Movie.mkv")
	mediaWriteTestFile(t, video, "video")
	mediaWriteTestFile(t, filepath.Join(dir, "Movie.chs.srt"), "srt")
	mediaWriteTestFile(t, filepath.Join(dir, "Movie.eng.ass"), "ass")
	mediaWriteTestFile(t, filepath.Join(dir, "Movie.keep.vtt"), "vtt")
	mediaWriteTestFile(t, filepath.Join(dir, "Movie.s3.zho.sup"), "pgs")
	mediaWriteTestFile(t, filepath.Join(dir, "Movie2.chs.srt"), "not mine")

	work := mediaExternalSubtitleWork(video)
	if len(work) != 2 {
		t.Fatalf("应转换两个外挂字幕，得到 %d", len(work))
	}
	for _, sub := range work {
		if sub.SourcePath == "" || filepath.Ext(sub.FinalPath) != ".vtt" || sub.TempPath != sub.FinalPath+".fcwork" {
			t.Fatalf("外挂输出不对: %+v", sub)
		}
		args := strings.Join(buildMediaExternalArgs(sub.SourcePath, sub), " ")
		if !strings.Contains(args, sub.SourcePath) || !strings.Contains(args, "-c:s webvtt") {
			t.Fatalf("外挂转换命令不对: %s", args)
		}
	}
	probe := mediaTestProbe("h264", []string{"aac"})
	subs := mediaScanSubtitles(video, &probe)
	if len(subs) != 2 {
		t.Fatalf("扫描只应列 VTT/SUP: %+v", subs)
	}
	for _, sub := range subs {
		ext := filepath.Ext(sub.Path)
		if ext != ".vtt" && ext != ".sup" {
			t.Fatalf("列出了不应展示的字幕: %+v", sub)
		}
	}

	mediaWriteTestFile(t, strings.TrimSuffix(work[0].SourcePath, filepath.Ext(work[0].SourcePath))+".vtt", "existing")
	if got := len(mediaExternalSubtitleWork(video)); got != 1 {
		t.Fatalf("已有 VTT 不应重复转换，剩余 = %d", got)
	}
}

func mediaArgsContain(args []string, values ...string) bool {
	joined := strings.Join(args, " ")
	return strings.Contains(joined, strings.Join(values, " "))
}

func TestMediaProcPlanReusesExtractedSubtitle(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "show.mkv")
	mediaWriteTestFile(t, source, "video")
	mediaWriteTestFile(t, filepath.Join(dir, "show.s2.zho.vtt"), "existing subtitle")
	plan, err := buildMediaPlan(source, mediaTestProbe("h264", []string{"aac"}, "subrip"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Subtitles) != 0 || plan.hasWork() {
		t.Fatalf("完整字幕不应在每次全库检查时重复生成: %+v", plan.Subtitles)
	}
}

func mediaWriteTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
