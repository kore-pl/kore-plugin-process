//go:build linux

// 并发：用真实 goroutine 同时 kill 同一受控进程、同时扫描端口 / fd、同时读写
// history / trend state。断言不 panic、结果契约稳定；数据竞争由
// `go test -race ./tests/boundary/ -count=1` 直接判负。
package boundary

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pengtang1024/kore-plugin-process/internal/procfs"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestConcurrentKillSameOwnedProcess 8 个 goroutine 同时 kill 同一个自己起的子进程：
// 允许一个成功 + 其余 E_NOTFOUND（进程已退出），但不得 panic、不得误杀或返回其它错误码。
func TestConcurrentKillSameOwnedProcess(t *testing.T) {
	sleeper := spawnOwnedSleep(t)
	pid := sleeper.Process.Pid
	name := procComm(t, pid)
	rt := newRuntime(t, "")
	params := fmt.Sprintf(`{"pid":%d,"confirm":true,"name":%q,"signal":"TERM"}`, pid, name)

	const workers = 8
	start := make(chan struct{})
	errs := make([]*pluginapi.Error, workers)
	killed := make([]bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, perr := rt.Call("process.kill", json.RawMessage(params))
			if perr != nil {
				errs[index] = perr
				return
			}
			var decoded struct {
				Killed bool `json:"killed"`
			}
			if err := json.Unmarshal(result, &decoded); err != nil {
				errs[index] = &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: "kill 结果解码失败: " + err.Error()}
				return
			}
			killed[index] = decoded.Killed
		}(i)
	}
	close(start)
	wg.Wait()

	success := 0
	for i := 0; i < workers; i++ {
		if errs[i] == nil {
			if !killed[i] {
				t.Errorf("worker %d 返回 killed=false", i)
			}
			success++
			continue
		}
		if errs[i].Code != pluginapi.E_NOTFOUND {
			t.Errorf("worker %d 并发 kill 错误码=%s（期望成功或 E_NOTFOUND）：%s", i, errs[i].Code, errs[i].Message)
		}
	}
	if success == 0 {
		t.Fatalf("%d 个并发 kill 无一成功，契约不成立", workers)
	}
	t.Logf("并发 kill 同一进程 pid=%d：成功 %d/%d，其余按 E_NOTFOUND 收敛", pid, success, workers)
}

// TestConcurrentPortsAndFDScan 并发查询指定进程 / 自身 / 全系统的端口与 fd 归属，
// 断言每次都返回合法 process.ports.v1 结构，而不是共享 map 写坏或 panic。
func TestConcurrentPortsAndFDScan(t *testing.T) {
	sleeper := spawnOwnedSleep(t)
	rt := newRuntime(t, "")
	targets := []string{
		fmt.Sprintf(`{"pid":%d}`, sleeper.Process.Pid),
		fmt.Sprintf(`{"pid":%d}`, os.Getpid()),
		`{}`, // 全系统扫描：最吃 /proc 遍历与 fd 读取的路径
	}

	const workers = 6
	start := make(chan struct{})
	errs := make([]*pluginapi.Error, workers)
	scanned := make([]int, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			for _, params := range targets {
				result, perr := rt.Call("process.ports", json.RawMessage(params))
				if perr != nil {
					errs[index] = perr
					return
				}
				var decoded struct {
					Schema           string `json:"schema"`
					ScannedProcesses int    `json:"scanned_processes"`
				}
				if err := json.Unmarshal(result, &decoded); err != nil {
					errs[index] = &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: "ports 结果解码失败: " + err.Error()}
					return
				}
				if decoded.Schema != "process.ports.v1" {
					errs[index] = &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: "schema=" + decoded.Schema}
					return
				}
				scanned[index] += decoded.ScannedProcesses
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d 并发 ports 失败：%+v", i, errs[i])
		}
		if scanned[i] <= 0 {
			t.Fatalf("worker %d 未扫描到任何进程（scanned=%d）", i, scanned[i])
		}
	}
}

// TestConcurrentHistoryAndTrendState 并发读写同一 StateDir 下的 history / trend state：
// stateMu 串行化后计数必须连续（无丢更新），最终落盘 JSON 可解析且点数与调用次数一致。
func TestConcurrentHistoryAndTrendState(t *testing.T) {
	sleeper := spawnOwnedSleep(t)
	pid := sleeper.Process.Pid
	stateDir := t.TempDir()

	base := time.Now()
	var clock atomic.Int64
	rt := procfs.NewRuntime(procfs.Options{
		ProcRoot:  realProcRoot,
		SelfPID:   os.Getpid(),
		ParentPID: os.Getppid(),
		MaskArgs:  true,
		GOOS:      "linux",
		StateDir:  stateDir,
		Now: func() time.Time {
			// 每次调用推进 31s：history 跨过 30s 采样间隔，trend 跨过 2s 差分窗口。
			return base.Add(time.Duration(clock.Add(31)) * time.Second)
		},
	}, nil)

	const historyCalls, trendCalls = 4, 4
	start := make(chan struct{})
	historyCounts := make([]int, historyCalls)
	trendCounts := make([]int, trendCalls)
	errs := make([]*pluginapi.Error, historyCalls+trendCalls)
	var wg sync.WaitGroup

	record := func(slot int, perr *pluginapi.Error) {
		if perr != nil {
			errs[slot] = perr
		}
	}
	for i := 0; i < historyCalls; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, perr := rt.Call("process.history", json.RawMessage(`{}`))
			if perr != nil {
				record(index, perr)
				return
			}
			var decoded struct {
				Count   int  `json:"count"`
				Sampled bool `json:"sampled"`
			}
			if err := json.Unmarshal(result, &decoded); err != nil {
				record(index, &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: err.Error()})
				return
			}
			if !decoded.Sampled {
				record(index, &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: "跨 30s 间隔却未产生新采样"})
				return
			}
			historyCounts[index] = decoded.Count
		}(i)
	}
	for i := 0; i < trendCalls; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			params := fmt.Sprintf(`{"pid":%d,"window":10}`, pid)
			result, perr := rt.Call("process.trend", json.RawMessage(params))
			if perr != nil {
				record(historyCalls+index, perr)
				return
			}
			var decoded struct {
				Count int `json:"count"`
			}
			if err := json.Unmarshal(result, &decoded); err != nil {
				record(historyCalls+index, &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: err.Error()})
				return
			}
			trendCounts[index] = decoded.Count
		}(i)
	}
	close(start)
	wg.Wait()

	for i, perr := range errs {
		if perr != nil {
			t.Fatalf("并发 worker %d 失败：%+v", i, perr)
		}
	}

	sortedHistory := append([]int(nil), historyCounts...)
	sort.Ints(sortedHistory)
	for i, count := range sortedHistory {
		if count != i+1 {
			t.Fatalf("history 计数不连续（有丢更新）：%v", sortedHistory)
		}
	}

	sortedTrend := append([]int(nil), trendCounts...)
	sort.Ints(sortedTrend)
	for i, count := range sortedTrend {
		if count != i {
			t.Fatalf("trend 计数不连续（首次基线应 0，其后递增）：%v", sortedTrend)
		}
	}

	historyData, err := os.ReadFile(filepath.Join(stateDir, "history.json"))
	if err != nil {
		t.Fatalf("读取 history.json: %v", err)
	}
	var historyFile struct {
		Points []json.RawMessage `json:"points"`
	}
	if err := json.Unmarshal(historyData, &historyFile); err != nil {
		t.Fatalf("history.json 解析失败: %v", err)
	}
	if len(historyFile.Points) != historyCalls {
		t.Fatalf("history 落盘点数=%d，期望 %d", len(historyFile.Points), historyCalls)
	}

	trendData, err := os.ReadFile(filepath.Join(stateDir, "trend.json"))
	if err != nil {
		t.Fatalf("读取 trend.json: %v", err)
	}
	var trendFile struct {
		Procs map[string]struct {
			Points []json.RawMessage `json:"points"`
		} `json:"procs"`
	}
	if err := json.Unmarshal(trendData, &trendFile); err != nil {
		t.Fatalf("trend.json 解析失败: %v", err)
	}
	proc, ok := trendFile.Procs[strconv.Itoa(pid)]
	if !ok {
		t.Fatalf("trend.json 缺 pid=%d 的记录", pid)
	}
	if len(proc.Points) != trendCalls-1 {
		t.Fatalf("trend 落盘点数=%d，期望 %d", len(proc.Points), trendCalls-1)
	}
}
