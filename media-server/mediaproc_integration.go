package main

import (
	"sync"
	"time"
)

var streamRequests = struct {
	sync.Mutex
	paths map[string]time.Time
}{paths: map[string]time.Time{}}

func noteStreamRequest(path string) {
	streamRequests.Lock()
	streamRequests.paths[path] = time.Now()
	streamRequests.Unlock()
}

func mediaStreamRecent(path string, within time.Duration) bool {
	streamRequests.Lock()
	at := streamRequests.paths[path]
	streamRequests.Unlock()
	return !at.IsZero() && time.Since(at) < within
}

func mediaProcBusyReason() string {
	for _, t := range animeTaskList() {
		if t.Status == "running" || t.Status == "processing" {
			return "等番剧下载和拼接完成再开始转换"
		}
	}
	if bt != nil {
		bt.mu.Lock()
		processing := false
		for _, t := range bt.tasks {
			if t.Status == "processing" {
				processing = true
				break
			}
		}
		bt.mu.Unlock()
		if processing {
			return "等下载文件整理完再开始转换"
		}
	}
	if uploadsActive() {
		return "等正在上传的片子传完再开始转换"
	}
	return ""
}
