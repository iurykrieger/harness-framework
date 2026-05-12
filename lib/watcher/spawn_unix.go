//go:build darwin || linux

package watcher

import "syscall"

var sysProcAttr = syscall.SysProcAttr{Setsid: true}
