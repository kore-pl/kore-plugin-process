//go:build linux

package e2e

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestMain 让本测试二进制可以充当"自定义 argv 的长睡进程"：
// 以 argv=["sleep","300","--password=secret"] 启动时，/proc cmdline 与真实 sleep
// 完全一致，用于验证脱敏链路（GNU sleep 本身不接受该参数）。
func TestMain(m *testing.M) {
	if os.Getenv("PROCESS_E2E_SLEEPER") == "1" {
		time.Sleep(300 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// buildPlugin 构建真实插件二进制到临时目录。
func buildPlugin(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "process-plugin")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建插件失败: %v\n%s", err, out)
	}
	return binary
}

// startPlugin 以协议环境变量启动插件进程；stderr 收集起来便于失败时定位。
func startPlugin(t *testing.T, binary, socket string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	stderr := &bytes.Buffer{}
	process := exec.Command(binary)
	process.Env = append(os.Environ(),
		"KORE_PLUGIN_SOCK="+socket,
		"KORE_PLUGIN_ID=process",
		"KORE_PLUGIN_TOKEN=e2e-token",
	)
	process.Stdout = io.Discard
	process.Stderr = stderr
	if err := process.Start(); err != nil {
		t.Fatalf("启动插件失败: %v", err)
	}
	t.Cleanup(func() {
		if process.ProcessState == nil {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
	})
	return process, stderr
}

// spawnRealSleep 启动真实 /bin/sleep 300 子进程（comm=sleep）。
func spawnRealSleep(t *testing.T) *exec.Cmd {
	t.Helper()
	path, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("找不到 sleep 命令: %v", err)
	}
	command := exec.Command(path, "300")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatalf("启动 sleep 失败: %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	})
	waitExists(t, command.Process.Pid, 2*time.Second)
	return command
}

// spawnSelfSleeper 启动本测试二进制作为长睡进程，argv 由调用方指定（用于构造
// cmdline 恰为 `sleep 300 --password=secret` 的真实进程）。
func spawnSelfSleeper(t *testing.T, argv ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0])
	command.Args = argv
	command.Env = append(os.Environ(), "PROCESS_E2E_SLEEPER=1")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatalf("启动 self sleeper 失败: %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	})
	waitExists(t, command.Process.Pid, 2*time.Second)
	return command
}

// waitExists 等待 /proc/<pid> 出现。
func waitExists(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(procPath(pid)); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d 在 %s 内未出现", pid, timeout)
}

// waitGone 等待 /proc/<pid> 消失。
func waitGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(procPath(pid)); errors.Is(err, fs.ErrNotExist) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d 在 %s 内未消失", pid, timeout)
}

// procPath 拼接 /proc/<pid>。
func procPath(pid int) string { return filepath.Join("/proc", strconv.Itoa(pid)) }
