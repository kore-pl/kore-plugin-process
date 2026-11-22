//go:build linux

package procfs

import (
	"errors"
	"syscall"
)

// signalNumber 把白名单信号名映射为 Linux 信号编号；入参已由 ParseSignal 校验。
func signalNumber(sig Signal) syscall.Signal {
	switch sig {
	case SignalKILL:
		return syscall.SIGKILL
	case SignalHUP:
		return syscall.SIGHUP
	case SignalINT:
		return syscall.SIGINT
	default:
		return syscall.SIGTERM
	}
}

// sendSignal 向目标 pid 投递信号；EPERM 由上层映射为 E_UNAUTHORIZED。
func sendSignal(pid int, sig Signal) error {
	return syscall.Kill(pid, signalNumber(sig))
}

// isNoSuchProcess 判断 errno 是否为 ESRCH（目标进程不存在）。
func isNoSuchProcess(err error) bool { return errors.Is(err, syscall.ESRCH) }
