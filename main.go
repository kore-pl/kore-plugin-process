// Package main 是 kore 的 process 插件：真实进程列表 / 详情 / 信号 / 进程树 /
// 优先级调整，并对 init、kored、kore 组件与内核线程做统一保护。
//
// 真实实现只读 Linux /proc：不读 /proc/kcore、不接受任意 /proc 路径，所有路径都由
// 固定根 + 数字 pid + 固定文件名拼成。CPU 采样零 ticker——只在 process.list 调用时
// 更新基线并做 utime+stime 差分。
//
// 协议面（kore.plugin.v1）：连接内核注入的 UDS → kore.hello 握手 → 会话循环读 request、
// 按 method 分发并回 response；stdout 留给协议，日志一律写 stderr。
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/pengtang1024/kore-plugin-process/internal/procfs"
)

// main 按退出码区分失败：0 优雅退出、1 启动或协议失败。
func main() {
	logger := newLogger(os.Getenv("KORE_LOG_LEVEL"))
	if err := run(logger); err != nil {
		logger.Error("插件退出", "err", err)
		os.Exit(1)
	}
}

// run 依次完成配置、建连、握手与会话循环；收到 kore.shutdown 后返回 nil。
func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	selfExe, _ := os.Executable()
	procRuntime := procfs.NewRuntime(procfs.Options{
		ProcRoot:      procfs.DefaultProcRoot,
		SelfPID:       os.Getpid(),
		ParentPID:     os.Getppid(),
		SocketDir:     filepath.Dir(cfg.socket),
		SelfExe:       selfExe,
		MaskArgs:      cfg.maskArgs,
		ProtectedPIDs: cfg.protectedPIDs,
	}, nil)
	conn, err := net.Dial("unix", cfg.socket)
	if err != nil {
		return fmt.Errorf("连接主壳 socket %s: %w", cfg.socket, err)
	}
	defer func() { _ = conn.Close() }()
	if err := handshake(conn, cfg); err != nil {
		return err
	}
	logger.Info("hello 完成", "plugin_id", cfg.id, "version", cfg.version, "mask_args", cfg.maskArgs)
	return serve(conn, cfg, procRuntime, logger)
}
