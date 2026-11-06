//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func mediaProcFreeSpace(path string) (int64, error) {
	_, free, err := windowsDiskSpace(filepath.Dir(path))
	return free, err
}

func mediaProcPrepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

func mediaProcNice(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(cmd.Process.Pid))
	if err == nil {
		defer windows.CloseHandle(handle)
		_ = windows.SetPriorityClass(handle, windows.BELOW_NORMAL_PRIORITY_CLASS)
	}
}

func mediaProcKillRecorded(pid int, temps []string) error {
	if pid <= 0 {
		return nil
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(handle)
	// 核对和终止使用同一进程句柄，避免进程退出后 PID 被其他程序复用。
	if !mediaProcOwnedWindowsProcess(handle, temps) {
		return nil
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return err
	}
	result, err := windows.WaitForSingleObject(handle, 5000)
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("等待被打断的转码进程退出超时")
	}
	return nil
}

func mediaProcRecordedCommand(pid int, temps []string) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	return mediaProcOwnedWindowsProcess(handle, temps)
}

func mediaProcOwnedWindowsProcess(handle windows.Handle, temps []string) bool {
	exe := make([]uint16, 32768)
	size := uint32(len(exe))
	if windows.QueryFullProcessImageName(handle, 0, &exe[0], &size) != nil {
		return false
	}
	configured := currentConfig().Media.FFmpegPath
	if configured == "" {
		configured = filepath.Join(exeDir(), "ffmpeg.exe")
	}
	configured, err := filepath.Abs(configured)
	if err != nil || !strings.EqualFold(filepath.Clean(windows.UTF16ToString(exe[:size])), filepath.Clean(configured)) {
		return false
	}
	var needed uint32
	_ = windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation, nil, 0, &needed)
	if needed < uint32(unsafe.Sizeof(windows.NTUnicodeString{})) || needed > 1<<20 {
		return false
	}
	data := make([]byte, needed)
	if windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation, unsafe.Pointer(&data[0]), needed, &needed) != nil {
		return false
	}
	command := (*windows.NTUnicodeString)(unsafe.Pointer(&data[0]))
	if command.Buffer == nil || command.Length == 0 || command.Length%2 != 0 {
		return false
	}
	// 原生 API 返回的字符串必须位于本次分配的缓冲区中。
	start := uintptr(unsafe.Pointer(command.Buffer))
	lo := uintptr(unsafe.Pointer(&data[0]))
	if start < lo || start+uintptr(command.Length) > lo+uintptr(len(data)) {
		return false
	}
	line := windows.UTF16ToString(unsafe.Slice(command.Buffer, int(command.Length)/2))
	args, err := windows.DecomposeCommandLine(line)
	if err != nil || len(args) == 0 {
		return false
	}
	for _, temp := range temps {
		if !strings.HasSuffix(temp, ".fcwork") {
			continue
		}
		for _, arg := range args[1:] {
			if strings.EqualFold(filepath.Clean(arg), filepath.Clean(temp)) {
				return true
			}
		}
	}
	return false
}
