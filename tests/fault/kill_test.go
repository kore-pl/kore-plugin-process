//go:build linux

package fault

import (
	"encoding/json"
	"testing"

	"github.com/pengtang1024/kore-plugin-process/internal/procfs"
)

// TestKillFault 注入 kill -9（进程被信号终止），期望四个方法返回 E_UPSTREAM。
func TestKillFault(t *testing.T) {
	assertFault(t, procfs.FaultKill, "E_UPSTREAM", "信号 9")
}

// TestKillFaultContract 单独验证 kill 故障的错误体结构完整。
func TestKillFaultContract(t *testing.T) {
	rt := newRuntime(t, faultSource{kind: procfs.FaultKill})
	_, perr := rt.Call("process.detail", json.RawMessage(`{"pid":4242}`))
	verifyErrorContract(t, perr)
}
