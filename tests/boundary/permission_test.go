//go:build linux

// 权限不足：真实访问非自己拥有的 /proc/<pid>（/proc/1/fd 为 root 0500），
// 以及 `chmod 000` 的目标（stat 不可读 / StateDir 不可读）。断言插件按 ADR-0018
// 明确降级——要么返回 E_NEED_ROOT / E_UNAUTHORIZED，要么降级为 restricted，
// 不允许 panic、伪成功或把 EACCES 说成「进程不存在」。
package boundary

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/pengtang1024/kore-plugin-process/internal/procfs"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestPermissionRealProcOtherUID 访问 root 拥有的 /proc/1/fd：非特权用户读不到，
// ports 必须返回 E_NEED_ROOT 且附可操作提示，而不是空结果。
func TestPermissionRealProcOtherUID(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 可读全部 /proc，无法构造非本用户场景")
	}
	if _, err := os.ReadDir("/proc/1/fd"); err == nil {
		t.Skip("/proc/1/fd 当前可读（hidepid / 容器差异），跳过")
	} else if !errors.Is(err, fs.ErrPermission) {
		t.Skipf("/proc/1/fd 错误不是 EACCES（%v），跳过", err)
	}

	rt := newRuntime(t, "")
	perr := callError(t, rt, "process.ports", `{"pid":1}`)
	if perr.Code != pluginapi.E_NEED_ROOT {
		t.Fatalf("访问非本用户 /proc/1/fd 错误码=%q，期望 E_NEED_ROOT（message=%q）", perr.Code, perr.Message)
	}
	if !contains(perr.Message, "root") {
		t.Fatalf("E_NEED_ROOT 未附提权提示：%q", perr.Message)
	}
	t.Logf("权限降级：/proc/1/fd → %s：%s", perr.Code, perr.Message)
}

// TestPermissionDegradedUnreadableProcTarget chmod 000 的 stat 目标：单进程读取失败
// 不应让整个请求失败，而是返回 restricted=true + 明确原因。
func TestPermissionDegradedUnreadableProcTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受 chmod 000 限制，无法构造权限不足场景")
	}

	root := t.TempDir()
	pidDir := filepath.Join(root, "4242")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatalf("建假 proc 目录: %v", err)
	}
	statPath := filepath.Join(pidDir, "stat")
	if err := os.WriteFile(statPath, []byte("4242 (victim) S 1 4242 4242 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 1 0 0\n"), 0o644); err != nil {
		t.Fatalf("写假 stat: %v", err)
	}
	if err := os.Chmod(statPath, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}

	rt := procfs.NewRuntime(procfs.Options{
		ProcRoot: root, SelfPID: os.Getpid(), MaskArgs: true, GOOS: "linux",
	}, nil)
	raw, perr := rt.Call("process.detail", json.RawMessage(`{"pid":4242}`))
	if perr != nil {
		t.Fatalf("chmod 000 目标不应整体失败，应降级为 restricted：%+v", perr)
	}
	var decoded struct {
		PID              int    `json:"pid"`
		Restricted       bool   `json:"restricted"`
		RestrictedReason string `json:"restricted_reason"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("解析 detail 结果: %v", err)
	}
	if decoded.PID != 4242 || !decoded.Restricted {
		t.Fatalf("未按预期降级：%+v", decoded)
	}
	if !contains(decoded.RestrictedReason, "stat 不可读") {
		t.Fatalf("restricted_reason 未指明原因：%q", decoded.RestrictedReason)
	}
	t.Logf("权限降级：pid=4242 restricted=true reason=%q", decoded.RestrictedReason)
}

// TestPermissionDeniedStateDir chmod 000 的 StateDir：history 读写必须返回
// E_UNAUTHORIZED，而不是静默丢弃历史或写伪数据。
func TestPermissionDeniedStateDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受 chmod 000 限制，无法构造权限不足场景")
	}

	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o000); err != nil {
		t.Fatalf("chmod 000 StateDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })

	rt := newRuntime(t, stateDir)
	perr := callError(t, rt, "process.history", `{}`)
	if perr.Code != pluginapi.E_UNAUTHORIZED {
		t.Fatalf("StateDir 不可读错误码=%q，期望 E_UNAUTHORIZED（message=%q）", perr.Code, perr.Message)
	}
	if !contains(perr.Message, "权限") {
		t.Fatalf("权限错误未说明原因：%q", perr.Message)
	}
	t.Logf("权限不足：StateDir chmod 000 → %s：%s", perr.Code, perr.Message)
}
