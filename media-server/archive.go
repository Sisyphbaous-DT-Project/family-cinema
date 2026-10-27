package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
	"github.com/ulikunitz/xz"
)

// 压缩包扩展名（影视资源常见的打包格式）
var archiveExts = map[string]bool{
	".rar": true, ".zip": true, ".7z": true,
	".tar": true, ".gz": true, ".tgz": true,
	".bz2": true, ".tbz2": true, ".tbz": true,
	".xz": true, ".txz": true, ".lzma": true,
}

// 分卷压缩包的后续卷（.r00/.part2.rar/.z01 等），解压主卷时库会自动串联，不单独处理
// Go 的 regexp 不支持负向预查，partN 的首卷排除放在 isSecondaryVolume 里用代码判断
var (
	reVolumeRAR  = regexp.MustCompile(`(?i)\.r\d{2,3}$`)
	reVolumeZip  = regexp.MustCompile(`(?i)\.z\d{2,3}$`)
	reVolumePart = regexp.MustCompile(`(?i)\.part(\d+)\.rar$`)
	reVolumeSeq  = regexp.MustCompile(`(?i)\.\d{3}$`) // xxx.001/.002 分卷
	rePartSuffix = regexp.MustCompile(`(?i)\.part\d+$`)
)

func isArchive(name string) bool {
	return archiveExts[strings.ToLower(filepath.Ext(name))]
}

// isSecondaryVolume 判断是否为分卷的非首卷（这些由解压库自动串联，不单独作为入口）
func isSecondaryVolume(name string) bool {
	if reVolumeRAR.MatchString(name) || reVolumeZip.MatchString(name) || reVolumeSeq.MatchString(name) {
		return true
	}
	// part1.rar 是首卷，part2 及以后才是后续卷
	if m := reVolumePart.FindStringSubmatch(name); m != nil {
		n, err := strconv.Atoi(m[1])
		return err == nil && n > 1
	}
	return false
}

// extractResult 解压结果
type extractResult struct {
	Videos  []string // 解出的视频文件绝对路径
	Others  int      // 跳过的非视频文件数
	Skipped []string // 因异常跳过的条目
}

// ExtractArchive 解压压缩包到 destDir，只提取视频文件（避免把垃圾文件铺满媒体库）
func ExtractArchive(archivePath, destDir string) (*extractResult, error) {
	ext := strings.ToLower(filepath.Ext(archivePath))
	switch ext {
	case ".rar":
		return extractRAR(archivePath, destDir)
	case ".zip":
		return extractZIP(archivePath, destDir)
	case ".7z":
		return extract7Z(archivePath, destDir)
	case ".tar":
		return extractTarStream(archivePath, destDir, "")
	case ".gz", ".tgz":
		return extractTarStream(archivePath, destDir, "gzip")
	case ".bz2", ".tbz2", ".tbz":
		return extractTarStream(archivePath, destDir, "bzip2")
	case ".xz", ".txz", ".lzma":
		return extractTarStream(archivePath, destDir, "xz")
	}
	return nil, fmt.Errorf("未知压缩格式: %s", ext)
}

// extract7Z 解压 7z（纯 Go 实现，支持 LZMA/LZMA2/BZip2/Deflate 等常见编码）
func extract7Z(path, destDir string) (*extractResult, error) {
	zr, err := sevenzip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	res := &extractResult{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(f.Name)
		if !videoExts[strings.ToLower(filepath.Ext(name))] {
			res.Others++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			res.Skipped = append(res.Skipped, name)
			continue
		}
		err = writeExtracted(rc, destDir, name, res)
		rc.Close()
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

func extractRAR(path, destDir string) (*extractResult, error) {
	// rardecode 会自动识别并串联分卷
	rr, err := rardecode.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer rr.Close()

	res := &extractResult{}
	for {
		hdr, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, err
		}
		if hdr.IsDir {
			continue
		}
		name := filepath.Base(hdr.Name)
		if !videoExts[strings.ToLower(filepath.Ext(name))] {
			res.Others++
			continue
		}
		if err := writeExtracted(rr, destDir, name, res); err != nil {
			return res, err
		}
	}
	return res, nil
}

func extractZIP(path, destDir string) (*extractResult, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	res := &extractResult{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(f.Name)
		if !videoExts[strings.ToLower(filepath.Ext(name))] {
			res.Others++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			res.Skipped = append(res.Skipped, name)
			continue
		}
		err = writeExtracted(rc, destDir, name, res)
		rc.Close()
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

func extractTarStream(path, destDir, compress string) (*extractResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var r io.Reader = f
	switch compress {
	case "gzip":
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	case "bzip2":
		r = bzip2.NewReader(f)
	case "xz":
		xr, err := xz.NewReader(f)
		if err != nil {
			return nil, err
		}
		r = xr
	}

	// gz/xz/bz2 可能是 tar 包，也可能是单文件压缩（video.mkv.gz）。
	// 用 bufio 预读 tar 魔数判断，避免读走的字节无法回退。
	br := bufio.NewReaderSize(r, 1<<16)
	if !looksLikeTar(br) {
		return extractSingleStream(br, path, destDir)
	}

	tr := tar.NewReader(br)
	res := &extractResult{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, err
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		name := filepath.Base(hdr.Name)
		if !videoExts[strings.ToLower(filepath.Ext(name))] {
			res.Others++
			continue
		}
		if err := writeExtracted(tr, destDir, name, res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// writeExtracted 把单个条目写到目标目录，重名时自动加后缀
func writeExtracted(r io.Reader, destDir, name string, res *extractResult) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	dst := filepath.Join(destDir, sanitizeFileName(name))
	// 重名避让
	if _, err := os.Stat(dst); err == nil {
		base := strings.TrimSuffix(dst, filepath.Ext(dst))
		ext := filepath.Ext(dst)
		for i := 2; i < 100; i++ {
			cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
			if _, err := os.Stat(cand); os.IsNotExist(err) {
				dst = cand
				break
			}
		}
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	// 32KB 缓冲，盒子内存小，不要一次性读入
	buf := make([]byte, 32*1024)
	_, err = io.CopyBuffer(out, r, buf)
	closeErr := out.Close()
	if err != nil {
		os.Remove(dst)
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	res.Videos = append(res.Videos, dst)
	return nil
}

// looksLikeTar 预读判断解压后的流是否为 tar 归档。
// tar 头部第 257 字节起是 "ustar" 魔数（POSIX/GNU 两种变体）。
func looksLikeTar(br *bufio.Reader) bool {
	head, err := br.Peek(265)
	if err != nil || len(head) < 265 {
		return false
	}
	magic := string(head[257:262])
	return magic == "ustar"
}

// extractSingleStream 处理单文件压缩（如 movie.mkv.gz）：去掉压缩后缀作为文件名
func extractSingleStream(r io.Reader, archivePath, destDir string) (*extractResult, error) {
	name := strings.TrimSuffix(filepath.Base(archivePath), filepath.Ext(archivePath))
	// .tgz/.txz/.tbz 去掉后缀后要补回 .tar 语义，这里已判定非 tar，直接补 .mkv 兜底
	if filepath.Ext(name) == "" {
		name += ".mkv"
	}
	res := &extractResult{}
	if !videoExts[strings.ToLower(filepath.Ext(name))] {
		res.Others++
		return res, nil
	}
	if err := writeExtracted(r, destDir, name, res); err != nil {
		return res, err
	}
	return res, nil
}

func sanitizeFileName(name string) string {
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		`"`, "_", "<", "_", ">", "_", "|", "_")
	out := strings.TrimSpace(repl.Replace(name))
	if out == "" || out == "." || out == ".." {
		out = "unnamed"
	}
	return out
}

// VerifyVideo 轻量校验视频可用性：检查文件头魔数与最小体积。
// 盒子上没有 ffprobe，做完整解码校验不现实；这里只挡住"下载残缺/根本不是视频"的情况。
func VerifyVideo(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	// 小于 1MB 的视频文件基本可以判定为残件或占位文件
	if fi.Size() < 1<<20 {
		return fmt.Errorf("文件过小 (%s)，可能不完整", humanSize(fi.Size()))
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 16)
	n, err := io.ReadFull(f, head)
	if err != nil && n < 12 {
		return fmt.Errorf("读取文件头失败")
	}
	if !looksLikeVideoHeader(head, filepath.Ext(path)) {
		return fmt.Errorf("文件头异常，可能不是可播放的视频")
	}
	return nil
}

// looksLikeVideoHeader 按容器魔数判断
func looksLikeVideoHeader(head []byte, ext string) bool {
	if len(head) < 12 {
		return false
	}
	switch {
	// MP4/MOV/M4V: 第 4-8 字节为 ftyp
	case string(head[4:8]) == "ftyp":
		return true
	// Matroska/WebM: EBML 头 1A 45 DF A3
	case head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3:
		return true
	// AVI: RIFF....AVI
	case string(head[0:4]) == "RIFF" && string(head[8:11]) == "AVI":
		return true
	// FLV
	case string(head[0:3]) == "FLV":
		return true
	// MPEG-TS: 同步字节 0x47
	case head[0] == 0x47:
		return true
	// MPEG-PS / MPEG-1/2: 00 00 01 BA/B3
	case head[0] == 0 && head[1] == 0 && head[2] == 1:
		return true
	// RMVB: .RMF
	case string(head[0:4]) == ".RMF":
		return true
	// WMV/ASF: 30 26 B2 75
	case head[0] == 0x30 && head[1] == 0x26 && head[2] == 0xB2 && head[3] == 0x75:
		return true
	}
	// ISO 镜像等无固定小头的格式放行，交给播放器判断
	switch strings.ToLower(ext) {
	case ".iso", ".ts", ".m2ts":
		return true
	}
	return false
}

// ProcessDownloaded 下载完成后的自动整理：
//  1. 解压压缩包（含分卷），只提取视频
//  2. 校验解出的视频可用性
//  3. 校验通过则删除压缩包，失败则保留原包并报告
//
// 返回最终留在目录里的视频文件数量与处理说明。
func ProcessDownloaded(dir string, progress func(stage string)) (int, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, "", err
	}

	var archives []string
	videoCount := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if isArchive(name) && !isSecondaryVolume(name) {
			archives = append(archives, filepath.Join(dir, name))
		} else if videoExts[strings.ToLower(filepath.Ext(name))] {
			videoCount++
		}
	}

	if len(archives) == 0 {
		return videoCount, "", nil
	}

	var notes []string
	for _, ar := range archives {
		if progress != nil {
			progress("正在解压 " + filepath.Base(ar))
		}
		log.Printf("解压 %s", ar)
		res, err := ExtractArchive(ar, dir)
		if err != nil {
			notes = append(notes, filepath.Base(ar)+" 解压失败: "+err.Error())
			log.Printf("解压失败 %s: %v", ar, err)
			continue
		}
		if len(res.Videos) == 0 {
			notes = append(notes, filepath.Base(ar)+" 内未找到视频文件，已保留压缩包")
			continue
		}
		// 逐个校验解出的视频
		if progress != nil {
			progress("正在校验 " + filepath.Base(ar) + " 解出的文件")
		}
		allOK := true
		for _, v := range res.Videos {
			if err := VerifyVideo(v); err != nil {
				allOK = false
				notes = append(notes, filepath.Base(v)+" 校验未通过: "+err.Error())
				log.Printf("校验失败 %s: %v", v, err)
			} else {
				videoCount++
			}
		}
		if allOK {
			// 校验通过：删除主卷与所有分卷
			removeArchiveSet(ar)
			notes = append(notes, "已解压并清理 "+filepath.Base(ar))
		} else {
			notes = append(notes, filepath.Base(ar)+" 存在校验失败文件，保留压缩包备查")
		}
	}
	return videoCount, strings.Join(notes, "；"), nil
}

// removeArchiveSet 删除压缩包及其全部分卷（同主干名的 .rNN / .partN.rar / .zNN / .00N）
func removeArchiveSet(mainArchive string) {
	dir := filepath.Dir(mainArchive)
	base := filepath.Base(mainArchive)
	stem := archiveStem(base)

	os.Remove(mainArchive)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if n == base {
			continue
		}
		if archiveStem(n) == stem && (isArchive(n) || isSecondaryVolume(n)) {
			os.Remove(filepath.Join(dir, n))
		}
	}
}

// archiveStem 取压缩包主干名，剥掉扩展名与 .partN 后缀
func archiveStem(name string) string {
	s := strings.TrimSuffix(name, filepath.Ext(name))
	return rePartSuffix.ReplaceAllString(s, "")
}
