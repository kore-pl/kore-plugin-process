//go:build linux

package procfs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// treeRun 调用 process.tree 并解析成结构。
func treeRun(t *testing.T, rt *Runtime, params string) treeResult {
	t.Helper()
	raw, perr := rt.Call("process.tree", json.RawMessage(params))
	if perr != nil {
		t.Fatalf("process.tree(%s) 失败: %+v", params, perr)
	}
	var result treeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("解析 tree 结果失败: %v", err)
	}
	return result
}

// TestTreeReal 起 shell + 两个 sleep 形成真实父子链，断言 depth 与 ppid。
func TestTreeReal(t *testing.T) {
	shell := exec.Command("sh", "-c", "sleep 300 & sleep 300 & wait")
	if err := shell.Start(); err != nil {
		t.Fatalf("启动 sh 失败: %v", err)
	}
	pid := shell.Process.Pid
	waitExists(t, pid, 2*time.Second)
	t.Cleanup(func() {
		_ = shell.Process.Kill()
		_ = shell.Wait()
	})

	rt := NewRuntime(Options{SelfPID: os.Getpid(), MaskArgs: true}, nil)
	deadline := time.Now().Add(3 * time.Second)
	var result treeResult
	for {
		result = treeRun(t, rt, fmt.Sprintf(`{"pid":%d}`, pid))
		if result.Count >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("3s 内未看到两个子进程，nodes=%+v", result.Nodes)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if result.Root != pid || result.Nodes[0].PID != pid || result.Nodes[0].Depth != 0 {
		t.Fatalf("根节点不符: %+v", result.Nodes[0])
	}
	children := 0
	for _, node := range result.Nodes[1:] {
		if node.PPID == pid && node.Depth == 1 && node.Name == "sleep" {
			children++
			t.Cleanup(func() { _ = syscall.Kill(node.PID, syscall.SIGKILL) })
		}
	}
	if children != 2 {
		t.Fatalf("直接子进程数=%d，期望 2（nodes=%+v）", children, result.Nodes)
	}
	if result.Truncated {
		t.Fatalf("正常父子链不应被截断: %+v", result)
	}
}

// TestTreeDepthCap 深度链超过 10 层时截断且不越界。
func TestTreeDepthCap(t *testing.T) {
	records := make([]Record, 0, 20)
	for pid := 1; pid <= 20; pid++ {
		records = append(records, Record{PID: pid, PPID: pid - 1, Name: fmt.Sprintf("p%d", pid), State: 'S'})
	}
	rt := testRuntime(t, &fakeSource{records: records}, nil)
	result := treeRun(t, rt, `{"pid":1}`)
	if result.Count != MaxTreeDepth+1 {
		t.Fatalf("深度上限节点数=%d，期望 %d", result.Count, MaxTreeDepth+1)
	}
	if !result.Truncated {
		t.Fatal("超过深度上限应标记 truncated")
	}
	if result.Nodes[len(result.Nodes)-1].Depth != MaxTreeDepth {
		t.Fatalf("最深节点 depth=%d，期望 %d", result.Nodes[len(result.Nodes)-1].Depth, MaxTreeDepth)
	}
}

// TestTreeNodeCap 子进程数超过 500 时截断到 500 节点。
func TestTreeNodeCap(t *testing.T) {
	records := []Record{{PID: 1, PPID: 0, Name: "root", State: 'S'}}
	for pid := 2; pid <= MaxTreeNodes+100; pid++ {
		records = append(records, Record{PID: pid, PPID: 1, Name: "child", State: 'S'})
	}
	rt := testRuntime(t, &fakeSource{records: records}, nil)
	result := treeRun(t, rt, `{"pid":1}`)
	if result.Count != MaxTreeNodes {
		t.Fatalf("节点上限=%d，期望 %d", result.Count, MaxTreeNodes)
	}
	if !result.Truncated {
		t.Fatal("超过节点上限应标记 truncated")
	}
}

// TestTreeNotFound 根进程不存在返回 E_NOTFOUND。
func TestTreeNotFound(t *testing.T) {
	rt := testRuntime(t, &fakeSource{}, nil)
	if _, perr := rt.Call("process.tree", json.RawMessage(`{"pid":4242}`)); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("不存在根进程应返回 E_NOTFOUND，实际 %+v", perr)
	}
}
