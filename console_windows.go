//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// holdWindowIfLaunchedByClick keeps a double-clicked window open long enough to
// read the message that preceded it. Windows gives such a process its own
// console and destroys the window when the process exits, so an early failure
// (a missing tool, a bad config) flashes past unread. When Cairn is the only
// process on its console it was launched from Explorer, not from a shell; run
// from a terminal, this is a no-op.
func holdWindowIfLaunchedByClick() {
	var pids [4]uint32
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if n != 1 {
		return
	}
	fmt.Fprint(os.Stderr, "\nPress Enter to close this window…")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
