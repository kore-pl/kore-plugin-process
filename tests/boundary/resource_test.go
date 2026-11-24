//go:build linux

// 资源耗尽：在子进程里用真实内核限额注入，而不是伪造错误——
//
//	① ulimit -n（RLIMIT_NOFILE）耗尽 fd：process.list 要扫 /proc，是最吃 fd 的路径；
//	② ulimit -f（RLIMIT_FSIZE）耗尽可写文件大小：history 要落盘 state。
//
// 不选 ulimit -v（RLIMIT_AS）：Go 运行时在启动阶段就预留大块地址空间，限额会在运行时
// 初始化时直接杀掉进程，测不到插件的错误码。也不选 /dev/full：history 用「临时文件 +
// rename」原子写，符号链接目标会被 rename 替换，无法把唯一写路径定向到 /dev/full。
// 文件大小 / 打开文件数是同族内核限额，能真实触发 EMFILE / EFBIG，且插件仍能返回结构化错误。
package boundary

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

const resourceHelperEnv = "BOUNDARY_RESOURCE_HELPER"

// TestResourceExhaustionFileDescriptors 以 ulimit -n 64（软硬限同时收紧：Go 运行时会尝试把
// 软限抬回硬限，只收软限会被运行时撤销）启动子进程，子进程把 fd 全部占满后
// 调 process.list：必须返回 E_INTERNAL 而不是 panic；释放 fd 后同一 Runtime 必须恢复服务。
func TestResourceExhaustionFileDescriptors(t *testing.T) {
	out := runLimitedHelper(t, "nofile", `ulimit -n 64; exec "$0" "$@"`)
	for _, want := range []string{
		"BOUNDARY_NOFILE_LIMIT=64",
		"BOUNDARY_NOFILE_ERR=E_INTERNAL",
		"BOUNDARY_NOFILE_RECOVERED=ok",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("子进程输出缺少 %q：\n%s", want, out)
		}
	}
	t.Logf("资源耗尽（fd）：ulimit -n 64 注入后 list 返回 E_INTERNAL，释放 fd 后恢复")
}

// TestResourceExhaustionFileSize 以 ulimit -f 0 启动子进程：history 写 state 必须返回
// E_INTERNAL（写 history state 失败），恢复文件大小软限后同一 Runtime 必须写成功。
func TestResourceExhaustionFileSize(t *testing.T) {
	out := runLimitedHelper(t, "fsize", `ulimit -S -f 0; exec "$0" "$@"`)
	for _, want := range []string{
		"BOUNDARY_FSIZE_LIMIT=0",
		"BOUNDARY_FSIZE_ERR=E_INTERNAL",
		"BOUNDARY_FSIZE_RECOVERED=ok",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("子进程输出缺少 %q：\n%s", want, out)
		}
	}
	t.Logf("资源耗尽（文件大小）：ulimit -f 0 注入后 history 返回 E_INTERNAL，恢复限额后写成功")
}

// runLimitedHelper 用 sh 设置内核限额后 exec 测试二进制自身，只运行 helper 用例。
func runLimitedHelper(t *testing.T, mode, script string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", script, os.Args[0], "-test.run=^TestResourceExhaustionHelper$", "-test.v")
	cmd.Env = append(os.Environ(), resourceHelperEnv+"="+mode)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ulimit 子进程失败: %v\n%s", err, out)
	}
	return string(out)
}

// TestResourceExhaustionHelper 只由父测试以子进程方式运行；单独 go test 时跳过。
func TestResourceExhaustionHelper(t *testing.T) {
	mode := os.Getenv(resourceHelperEnv)
	if mode == "" {
		t.Skip("仅由 TestResourceExhaustion* 以子进程方式调用")
	}
	switch mode {
	case "nofile":
		helperNoFile(t)
	case "fsize":
		helperFileSize(t)
	default:
		t.Fatalf("未知资源注入模式 %q", mode)
	}
}

// helperNoFile 在 ulimit -n 限制下占满 fd，断言插件返回结构化错误且可恢复。
func helperNoFile(t *testing.T) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatalf("读取 RLIMIT_NOFILE: %v", err)
	}
	if limit.Cur != 64 {
		t.Fatalf("期望 ulimit -n 注入 64，实际软限 %d", limit.Cur)
	}
	t.Logf("BOUNDARY_NOFILE_LIMIT=%d", limit.Cur)

	var held []*os.File
	for {
		file, err := os.Open(os.DevNull)
		if err != nil {
			if !errors.Is(err, syscall.EMFILE) {
				t.Fatalf("占满 fd 时错误=%v，期望 EMFILE", err)
			}
			break
		}
		held = append(held, file)
	}
	if len(held) == 0 {
		t.Fatal("未占用任何 fd，限额注入无效")
	}

	rt := newRuntime(t, "")
	_, perr := rt.Call("process.list", json.RawMessage(`{"limit":5}`))
	if perr == nil {
		t.Fatalf("fd 耗尽时 process.list 应返回结构化错误，实际成功")
	}
	if perr.Code != pluginapi.E_INTERNAL {
		t.Fatalf("fd 耗尽错误码=%q，期望 E_INTERNAL（message=%q）", perr.Code, perr.Message)
	}
	t.Logf("BOUNDARY_NOFILE_ERR=%s message=%s", perr.Code, perr.Message)

	for _, file := range held {
		_ = file.Close()
	}
	result, perr := rt.Call("process.list", json.RawMessage(`{"limit":5}`))
	if perr != nil {
		t.Fatalf("释放 fd 后 process.list 仍失败：%+v", perr)
	}
	if len(result) == 0 {
		t.Fatal("释放 fd 后 process.list 返回空结果")
	}
	t.Logf("BOUNDARY_NOFILE_RECOVERED=ok")
}

// helperFileSize 在 ulimit -f 0 下写 history state，断言 EFBIG 被映射为 E_INTERNAL 且可恢复。
func helperFileSize(t *testing.T) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatalf("读取 RLIMIT_FSIZE: %v", err)
	}
	if limit.Cur != 0 {
		t.Fatalf("期望 ulimit -f 注入 0，实际软限 %d", limit.Cur)
	}
	t.Logf("BOUNDARY_FSIZE_LIMIT=%d", limit.Cur)

	// 写超限会触发 SIGXFSZ，默认动作是终止进程；忽略后才能拿到 EFBIG 错误码。
	signal.Ignore(syscall.SIGXFSZ)

	stateDir := t.TempDir()
	rt := newRuntime(t, stateDir)
	_, perr := rt.Call("process.history", json.RawMessage(`{}`))
	if perr == nil {
		t.Fatal("文件大小限额为 0 时 process.history 应返回结构化错误，实际成功")
	}
	if perr.Code != pluginapi.E_INTERNAL || !strings.Contains(perr.Message, "写 history state 失败") {
		t.Fatalf("文件大小耗尽错误=%+v，期望 E_INTERNAL 且含「写 history state 失败」", perr)
	}
	t.Logf("BOUNDARY_FSIZE_ERR=%s message=%s", perr.Code, perr.Message)

	restore := limit
	restore.Cur = restore.Max
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &restore); err != nil {
		t.Fatalf("恢复 RLIMIT_FSIZE 软限: %v", err)
	}
	result, perr := rt.Call("process.history", json.RawMessage(`{}`))
	if perr != nil {
		t.Fatalf("恢复文件大小限额后 process.history 仍失败：%+v", perr)
	}
	if len(result) == 0 {
		t.Fatal("恢复文件大小限额后 process.history 返回空结果")
	}
	t.Logf("BOUNDARY_FSIZE_RECOVERED=ok")
}
