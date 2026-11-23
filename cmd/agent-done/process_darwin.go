package main

/*
#include <libproc.h>
#include <sys/proc_info.h>
*/
import "C"

import (
	"strconv"
	"unsafe"
)

func processInfo(pid int) (C.struct_proc_bsdinfo, bool) {
	var info C.struct_proc_bsdinfo
	if pid <= 0 {
		return info, false
	}
	size := C.int(C.sizeof_struct_proc_bsdinfo)
	read := C.proc_pidinfo(C.int(pid), C.PROC_PIDTBSDINFO, 0, unsafe.Pointer(&info), size)
	return info, read == size
}

func startTicks(pid int) string {
	info, ok := processInfo(pid)
	if !ok {
		return ""
	}
	return strconv.FormatUint(uint64(info.pbi_start_tvsec), 10) + ":" +
		strconv.FormatUint(uint64(info.pbi_start_tvusec), 10)
}

func ancestors(pid int) []int {
	var chain []int
	for pid > 1 && !containsPID(chain, pid) {
		chain = append(chain, pid)
		info, ok := processInfo(pid)
		if !ok {
			break
		}
		pid = int(info.pbi_ppid)
	}
	return chain
}

// macOS tracks the launched CLI process. Its wrapper remains alive while the TUI runs.
func codexPID(launcher int) int { return launcher }
