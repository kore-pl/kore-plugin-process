//go:build linux

// Package boundary 承载 process 插件 production 判据① 的边界用例：
// 异常输入 / 并发 / 权限不足 / 资源耗尽 四类各至少一例，全部随 CI 的 make check
// （go test ./...）执行。并发用例用 sync.WaitGroup 起真实 goroutine，在
// `go test -race` 下断言无数据竞争。
package boundary

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-process/internal/procfs"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// realProcRoot 是真实内核执行面；边界用例刻意读真 /proc，而不是只对着假夹具断言。
const realProcRoot = "/proc"

// newRuntime 构造指向真实 /proc 的 Runtime。
func newRuntime(t *testing.T, stateDir string) *procfs.Runtime {
	t.Helper()
	return procfs.NewRuntime(procfs.Options{
		ProcRoot:  realProcRoot,
		SelfPID:   os.Getpid(),
		ParentPID: os.Getppid(),
		MaskArgs:  true,
		GOOS:      "linux",
		StateDir:  stateDir,
	}, nil)
}

// callError 执行一个方法并要求返回结构化错误（错误时不得带结果、错误体字段完整）。
func callError(t *testing.T, rt *procfs.Runtime, method, params string) *pluginapi.Error {
	t.Helper()
	result, perr := rt.Call(method, json.RawMessage(params))
	if perr == nil {
		t.Fatalf("%s(%s) 期望错误，实际成功：%s", method, params, result)
	}
	if result != nil {
		t.Fatalf("%s(%s) 出错时不应带结果：%s", method, params, result)
	}
	if perr.Code == "" || perr.Message == "" {
		t.Fatalf("%s(%s) 错误体字段不完整：%+v", method, params, perr)
	}
	return perr
}

// procComm 读真实 /proc/<pid>/comm，作为 kill 的防 PID 复用 name。
func procComm(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile(realProcRoot + "/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		t.Fatalf("读取 pid %d 的 comm 失败: %v", pid, err)
	}
	return strings.TrimSpace(string(data))
}

// spawnOwnedSleep 起一个本用例独占的 sleep 子进程。
// 安全边界：只允许 kill 自己起的测试进程，绝不把信号发给真实业务进程。
func spawnOwnedSleep(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动受控子进程失败: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}
