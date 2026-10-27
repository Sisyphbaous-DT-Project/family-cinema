package dl

import (
	"io"
	"os"
	"time"
)

// MergeProgress 拼接按已读取的原始分段字节计算；Finalizing 表示正在写 MP4 索引并落盘。
type MergeProgress struct {
	Done       int64
	Total      int64
	Finalizing bool
}

func mergeBytes(paths []string) int64 {
	var total int64
	for _, name := range paths {
		if st, err := os.Stat(name); err == nil {
			total += st.Size()
		}
	}
	return total
}

func reportMerge(report func(MergeProgress), p MergeProgress) {
	if report != nil {
		report(p)
	}
}

type mergeProgressReader struct {
	r      io.Reader
	total  int64
	done   int64
	last   time.Time
	report func(MergeProgress)
}

func (r *mergeProgressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.done += int64(n)
		now := time.Now()
		if r.done == r.total || now.Sub(r.last) >= 250*time.Millisecond {
			r.last = now
			reportMerge(r.report, MergeProgress{Done: r.done, Total: r.total})
		}
	}
	return n, err
}

func mergeReporter(opt Options, p Progress) func(MergeProgress) {
	return func(m MergeProgress) {
		p.Merging = true
		p.MergeDone, p.MergeTotal, p.MergeFinalizing = m.Done, m.Total, m.Finalizing
		opt.OnProgress(p)
	}
}
