//go:build !linux

package procfs

import "errors"

// errPlatformUnsupported 是非 Linux 平台的显式降级错误：process 只有 Linux 真实实现。
var errPlatformUnsupported = errors.New("process 插件只在 Linux 提供真实实现")

// signalNumber 在非 Linux 上不参与运行（Call 在入口就返回 E_UNSUPPORTED）。
func signalNumber(Signal) int { return 0 }

// sendSignal 在非 Linux 上显式失败，不静默降级。
func sendSignal(int, Signal) error { return errPlatformUnsupported }

// setNice 在非 Linux 上显式失败，不静默降级。
func setNice(int, int) error { return errPlatformUnsupported }

// isNoSuchProcess 在非 Linux 上没有 ESRCH 语义。
func isNoSuchProcess(error) bool { return false }
