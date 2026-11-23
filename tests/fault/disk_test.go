//go:build linux

package fault

import (
	"testing"

	"github.com/pengtang1024/kore-plugin-process/internal/procfs"
)

// TestDiskFullFault 注入磁盘满，期望五个方法返回 E_INTERNAL。
func TestDiskFullFault(t *testing.T) {
	assertFault(t, procfs.FaultDisk, "E_INTERNAL", "磁盘满")
}
