package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// mediaDirVideoBases 列出目录里所有视频的基名（不带扩展名），用来判断旁挂字幕归谁。
func mediaDirVideoBases(entries []os.DirEntry) []string {
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := filepath.Ext(name)
		if videoExts[strings.ToLower(ext)] {
			out = append(out, strings.TrimSuffix(name, ext))
		}
	}
	return out
}

// mediaHasNamePrefix 判断 name 是否就是 base，或以 "base." 开头（不分大小写）。
func mediaHasNamePrefix(name, base string) bool {
	if len(name) < len(base) || !strings.EqualFold(name[:len(base)], base) {
		return false
	}
	return len(name) == len(base) || name[len(base)] == '.'
}

// mediaSidecarRest 判断字幕文件是不是这个视频的，并返回去掉视频基名和扩展名后剩下的部分。
// 同一目录里还有更长且同样对得上的视频名（如 Film 和 Film.Extended）时，字幕归更长的那个。
func mediaSidecarRest(videoBase, name string, bases []string) (string, bool) {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if !mediaHasNamePrefix(stem, videoBase) {
		return "", false
	}
	for _, other := range bases {
		if len(other) > len(videoBase) && mediaHasNamePrefix(stem, other) {
			return "", false
		}
	}
	return strings.TrimPrefix(stem[len(videoBase):], "."), true
}

// mediaStreamToken 认出我们自己抽字幕时写进文件名的 "s3"，返回流序号；不是就返回 -1。
func mediaStreamToken(part string) int {
	if len(part) < 2 || part[0] != 's' {
		return -1
	}
	index, err := strconv.Atoi(part[1:])
	if err != nil || index < 0 {
		return -1
	}
	return index
}

// mediaSidecarLanguage 从视频名后面剩下的那段认语言：
// "s3.chi" 取 chi，"chs.forced" 取 chs，什么都没写就是 und。
func mediaSidecarLanguage(rest string) string {
	parts := strings.Split(rest, ".")
	if len(parts) > 1 && mediaStreamToken(parts[0]) >= 0 {
		return mediaSubtitleLanguage(parts[1])
	}
	for _, part := range parts {
		if part != "" && !strings.EqualFold(part, "forced") {
			return mediaSubtitleLanguage(part)
		}
	}
	return "und"
}

// mediaNaturalLess 按"数字按大小比"的方式比较文件名，s10 排在 s3 后面。
func mediaNaturalLess(a, b string) bool {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			i, j := 0, 0
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na, nb := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func mediaExternalSubtitleWork(path string) []mediaSubtitlePlan {
	dir := filepath.Dir(path)
	videoBase := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	bases := mediaDirVideoBases(entries)
	var out []mediaSubtitlePlan
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".srt" && ext != ".ass" && ext != ".ssa" {
			continue
		}
		rest, ok := mediaSidecarRest(videoBase, name, bases)
		if !ok {
			continue
		}
		source := filepath.Join(dir, name)
		final := strings.TrimSuffix(source, filepath.Ext(source)) + ".vtt"
		if _, err := os.Stat(final); err == nil {
			continue
		}
		out = append(out, mediaSubtitlePlan{
			Kind: "text", Lang: mediaSidecarLanguage(rest), Forced: strings.Contains(strings.ToLower(rest), "forced"),
			SourcePath: source, FinalPath: final, TempPath: final + ".fcwork", External: true,
		})
	}
	sort.Slice(out, func(i, j int) bool { return mediaNaturalLess(out[i].FinalPath, out[j].FinalPath) })
	return out
}

func buildMediaExternalArgs(source string, sub mediaSubtitlePlan) []string {
	args := []string{"-hide_banner", "-nostdin", "-y", "-v", "error", "-progress", "pipe:1", "-nostats"}
	input := source
	if sub.InputTemp != "" {
		input = sub.InputTemp
		// 临时副本以 .fcwork 结尾，要明说是什么格式
		format := "srt"
		if ext := strings.ToLower(filepath.Ext(source)); ext == ".ass" || ext == ".ssa" {
			format = "ass"
		}
		args = append(args, "-f", format)
	}
	return append(args, "-i", input, "-map", "0:s:0", "-c:s", "webvtt", "-f", "webvtt", sub.TempPath)
}

func mediaScanSubtitles(path string, probe *mediaProbe) []Subtitle {
	dir := filepath.Dir(path)
	videoBase := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	bases := mediaDirVideoBases(entries)
	streams := map[int]mediaProbeStream{}
	if probe != nil {
		for _, stream := range probe.Streams {
			streams[stream.Index] = stream
		}
	}
	var out []Subtitle
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".vtt" && ext != ".sup" {
			continue
		}
		rest, ok := mediaSidecarRest(videoBase, name, bases)
		if !ok {
			continue
		}
		sub := Subtitle{Path: filepath.Join(dir, name), Kind: "text", Lang: mediaSidecarLanguage(rest)}
		if ext == ".sup" {
			sub.Kind = "pgs"
		}
		if parts := strings.Split(rest, "."); len(parts) > 1 {
			if index := mediaStreamToken(parts[0]); index >= 0 {
				if stream, ok := streams[index]; ok {
					sub.Lang = mediaSubtitleLanguage(stream.Tags["language"])
					sub.Title = strings.TrimSpace(stream.Tags["title"])
					sub.Forced = stream.Disposition.Forced != 0
				}
			}
		}
		if strings.Contains(strings.ToLower(rest), "forced") {
			sub.Forced = true
		}
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return mediaNaturalLess(out[i].Path, out[j].Path) })
	return out
}

// mediaPrepareSubtitleInput 把不是 UTF-8 的外挂字幕（国内常见 GBK，也有 UTF-16）
// 转成 UTF-8 临时副本再交给 ffmpeg；原文件本来就是 UTF-8 时不建副本。原文件始终不动。
func mediaPrepareSubtitleInput(sub *mediaSubtitlePlan) error {
	info, err := os.Stat(sub.SourcePath)
	if err != nil {
		return fmt.Errorf("读不到字幕文件: %v", err)
	}
	if info.Size() > 64<<20 {
		return fmt.Errorf("字幕文件太大（%s），不像是字幕", humanSize(info.Size()))
	}
	data, err := os.ReadFile(sub.SourcePath)
	if err != nil {
		return fmt.Errorf("读不到字幕文件: %v", err)
	}
	text, converted, err := mediaSubtitleUTF8(data)
	if err != nil {
		return fmt.Errorf("字幕编码转换失败: %v", err)
	}
	if !converted {
		return nil
	}
	sub.InputTemp = sub.inputTempPath()
	return os.WriteFile(sub.InputTemp, text, 0o644)
}

// mediaSubtitleUTF8 返回 UTF-8 文本；第二个返回值表示是否做过转换。
func mediaSubtitleUTF8(data []byte) ([]byte, bool, error) {
	if bytes.HasPrefix(data, []byte{0xFF, 0xFE}) || bytes.HasPrefix(data, []byte{0xFE, 0xFF}) {
		out, _, err := transform.Bytes(xunicode.UTF16(xunicode.LittleEndian, xunicode.UseBOM).NewDecoder(), data)
		return out, true, err
	}
	if utf8.Valid(data) {
		return data, false, nil
	}
	out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), data)
	return out, true, err
}

// mediaGuessTextLanguage 文件名没写语言时，看字幕里假名、汉字多不多，猜日文或中文；
// 分不出就返回空。中英双语字幕的汉字也够多，会认成中文。
func mediaGuessTextLanguage(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 256<<10))
	han, kana, letters := 0, 0, 0
	for _, r := range string(data) {
		switch {
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			kana++
			letters++
		case unicode.Is(unicode.Han, r):
			han++
			letters++
		case unicode.IsLetter(r):
			letters++
		}
	}
	if letters < 20 {
		return ""
	}
	if kana*20 >= letters {
		return "jpn"
	}
	if han*10 >= letters {
		return "chi"
	}
	return ""
}

// mediaSubtitleOutputEmpty 判断 ffmpeg 正常结束后抽出的字幕是不是原轨本来就没内容：
// .sup 0 字节、.vtt 没有一条时间轴、.ass 没有一行 Dialogue。
func mediaSubtitleOutputEmpty(temp string) (bool, error) {
	info, err := os.Stat(temp)
	if err != nil {
		return false, err
	}
	if info.Size() == 0 {
		return true, nil
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSuffix(temp, ".fcwork")))
	if (ext != ".vtt" && ext != ".ass") || info.Size() > 4<<20 {
		return false, nil
	}
	data, err := os.ReadFile(temp)
	if err != nil {
		return false, err
	}
	if ext == ".vtt" {
		return !bytes.Contains(data, []byte("-->")), nil
	}
	return !bytes.Contains(bytes.ToLower(data), []byte("dialogue:")), nil
}
