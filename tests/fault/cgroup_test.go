//go:build linux

package fault

import (
	"testing"

	"github.com/pengtang1024/kore-plugin-process/internal/procfs"
)

// TestCgroupLimitFault 注入 cgroup 超限，期望四个方法返回 E_UNSUPPORTED。
func TestCgroupLimitFault(t *testing.T) {
	assertFault(t, procfs.FaultCgroup, "E_UNSUPPORTED", "cgroup")
}
