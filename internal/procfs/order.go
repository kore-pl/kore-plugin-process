package procfs

import (
	"sort"
	"strings"
)

// 允许的排序键。
const (
	SortCPU  = "cpu"
	SortMem  = "mem"
	SortPID  = "pid"
	SortName = "name"
)

// validSort 判断 process.list 的 sort 入参是否合法。
func validSort(key string) bool {
	switch key {
	case SortCPU, SortMem, SortPID, SortName:
		return true
	default:
		return false
	}
}

// sortRecords 按排序键排序：cpu / mem 降序，pid / name 升序。
// cpu 同值时按 pid 降序（新进程优先），mem/name 同值时按 pid 升序，保证输出顺序确定。
func sortRecords(records []Record, key string, cpu map[int]float64) {
	sort.SliceStable(records, func(i, j int) bool {
		left, right := records[i], records[j]
		switch key {
		case SortMem:
			if left.RSSKiB != right.RSSKiB {
				return left.RSSKiB > right.RSSKiB
			}
		case SortPID:
			return left.PID < right.PID
		case SortName:
			lname, rname := strings.ToLower(left.Name), strings.ToLower(right.Name)
			if lname != rname {
				return lname < rname
			}
		default: // cpu：降序；CPU 同为 0（或同值）时新进程（pid 大）优先
			if cpu[left.PID] != cpu[right.PID] {
				return cpu[left.PID] > cpu[right.PID]
			}
			return left.PID > right.PID
		}
		return left.PID < right.PID
	})
}
