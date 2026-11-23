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

// TestTreeSubtreeReal 起 shell + 两个 sleep 形成真实父子链，以 shell 为根断言 depth / ppid / children_count。
func TestTreeSubtreeReal(t *testing.T) {
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
		result = treeRun(t, rt, fmt.Sprintf(`{"root":%d}`, pid))
		if result.Count >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("3s 内未看到两个子进程，nodes=%+v", result.Nodes)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(result.Roots) != 1 || result.Roots[0] != pid || result.Nodes[0].PID != pid || result.Nodes[0].Depth != 0 {
		t.Fatalf("根节点不符: roots=%+v nodes0=%+v", result.Roots, result.Nodes[0])
	}
	if result.Nodes[0].ChildrenCount < 2 {
		t.Fatalf("根节点 children_count=%d，期望 ≥2", result.Nodes[0].ChildrenCount)
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

// TestTreeFullForest 空根参输出全系统森林：roots 非空、根节点的父进程不在扫描集、深度 0 起算。
func TestTreeFullForest(t *testing.T) {
	records := []Record{
		{PID: 1, PPID: 0, Name: "init", State: 'S'},
		{PID: 2, PPID: 0, Name: "kthreadd", State: 'S'},
		{PID: 10, PPID: 1, Name: "child-a", State: 'S'},
		{PID: 20, PPID: 999, Name: "orphan", State: 'S'},             // 父进程已退出：按根输出
		{PID: 30, PPID: 0, Name: "restrictedless", Restricted: true}, // 不可读记录不进树
	}
	rt := testRuntime(t, &fakeSource{records: records}, nil)
	result := treeRun(t, rt, `{}`)
	if len(result.Roots) != 3 || result.Roots[0] != 1 || result.Roots[1] != 2 || result.Roots[2] != 20 {
		t.Fatalf("roots=%+v，期望 [1 2 20]（升序、孤儿为根、restricted 不进树）", result.Roots)
	}
	if result.Total != 4 {
		t.Fatalf("total=%d，期望 4（排除 restricted）", result.Total)
	}
	depthOf := map[int]int{}
	for _, node := range result.Nodes {
		depthOf[node.PID] = node.Depth
	}
	if depthOf[1] != 0 || depthOf[10] != 1 || depthOf[20] != 0 {
		t.Fatalf("深度不符: %v", depthOf)
	}
}

// TestTreeCycleDoesNotHang PPID 成环（伪造执行面）不死循环、不重复输出，且孤立环不进 roots。
func TestTreeCycleDoesNotHang(t *testing.T) {
	done := make(chan treeResult, 1)
	go func() {
		records := []Record{
			{PID: 1, PPID: 0, Name: "init", State: 'S'},
			{PID: 50, PPID: 60, Name: "loop-a", State: 'S'},
			{PID: 60, PPID: 50, Name: "loop-b", State: 'S'},
			{PID: 70, PPID: 70, Name: "self-loop", State: 'S'}, // 自环
		}
		rt := testRuntime(t, &fakeSource{records: records}, nil)
		done <- treeRun(t, rt, `{}`)
	}()
	select {
	case result := <-done:
		seen := map[int]int{}
		for _, node := range result.Nodes {
			seen[node.PID]++
			if seen[node.PID] > 1 {
				t.Fatalf("节点 pid %d 重复输出: %+v", node.PID, result.Nodes)
			}
		}
		if len(result.Roots) != 1 || result.Roots[0] != 1 {
			t.Fatalf("roots=%+v，期望仅 [1]（环内进程的父进程在集合内，不是根）", result.Roots)
		}
		if seen[1] != 1 {
			t.Fatalf("init 应在输出中: %+v", result.Nodes)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PPID 成环触发死循环")
	}
}

// TestTreeCycleFromRoot 从根进入环也不死循环：环上第二个节点被 visited 拦下。
func TestTreeCycleFromRoot(t *testing.T) {
	records := []Record{
		{PID: 1, PPID: 0, Name: "init", State: 'S'},
		{PID: 2, PPID: 1, Name: "a", State: 'S'},
		{PID: 3, PPID: 2, Name: "b", State: 'S'},
	}
	// 构造环：3 的"子进程"是 1（ppid 表按父索引，令 1 的 ppid 也指向 3）
	records[0].PPID = 3
	rt := testRuntime(t, &fakeSource{records: records}, nil)
	result := treeRun(t, rt, `{"root":1}`)
	if !contains(fmt.Sprint(result.Nodes), "1") {
		t.Fatalf("环内根应输出: %+v", result.Nodes)
	}
	if result.Count > 3 {
		t.Fatalf("环不应无限展开: %+v", result.Nodes)
	}
}

// TestTreeDepthCap 深度链超过 10 层时截断且不越界。
func TestTreeDepthCap(t *testing.T) {
	records := make([]Record, 0, 20)
	for pid := 1; pid <= 20; pid++ {
		records = append(records, Record{PID: pid, PPID: pid - 1, Name: fmt.Sprintf("p%d", pid), State: 'S'})
	}
	rt := testRuntime(t, &fakeSource{records: records}, nil)
	result := treeRun(t, rt, `{"root":1}`)
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

// TestTreeNodeCap 子进程数超过 500 时截断到 500 节点（根 1 + 子进程 499）。
func TestTreeNodeCap(t *testing.T) {
	records := []Record{{PID: 1, PPID: 0, Name: "root", State: 'S', Cmdline: "root"}}
	for pid := 2; pid <= MaxTreeNodes+100; pid++ {
		records = append(records, Record{PID: pid, PPID: 1, Name: "child", State: 'S', Cmdline: "child"})
	}
	rt := testRuntime(t, &fakeSource{records: records}, nil)
	result := treeRun(t, rt, `{"root":1}`)
	if result.Count != MaxTreeNodes {
		t.Fatalf("节点上限=%d，期望 %d", result.Count, MaxTreeNodes)
	}
	if !result.Truncated {
		t.Fatal("超过节点上限应标记 truncated")
	}
	childrenTotal := MaxTreeNodes + 99 // pids 2..600
	if result.Nodes[0].ChildrenCount != childrenTotal {
		t.Fatalf("children_count=%d 应保留全扫描口径 %d", result.Nodes[0].ChildrenCount, childrenTotal)
	}
}

// TestTreeNotFound 根进程不存在返回 E_NOTFOUND；负 root 是非法入参。
func TestTreeNotFound(t *testing.T) {
	rt := testRuntime(t, &fakeSource{}, nil)
	if _, perr := rt.Call("process.tree", json.RawMessage(`{"root":4242}`)); perr == nil || perr.Code != pluginapi.E_NOTFOUND {
		t.Fatalf("不存在根进程应返回 E_NOTFOUND，实际 %+v", perr)
	}
	if _, perr := rt.Call("process.tree", json.RawMessage(`{"root":-1}`)); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("负 root 应返回 E_INVALID，实际 %+v", perr)
	}
}

// TestTreeRealFullSystem 真读本机 /proc 全树：节点数与 roots 均为真实数据（>100 进程的机器上必然截断）。
func TestTreeRealFullSystem(t *testing.T) {
	rt := NewRuntime(Options{SelfPID: os.Getpid(), MaskArgs: true}, nil)
	result := treeRun(t, rt, `{}`)
	if result.Count == 0 || len(result.Roots) == 0 {
		t.Fatalf("全树输出为空: roots=%v count=%d", result.Roots, result.Count)
	}
	if result.Count > MaxTreeNodes {
		t.Fatalf("节点数 %d 超上限", result.Count)
	}
	if result.Count == MaxTreeNodes && !result.Truncated {
		t.Fatal("命中节点上限必须标记 truncated")
	}
	byDepth0 := 0
	for _, node := range result.Nodes {
		if node.Depth == 0 {
			byDepth0++
		}
	}
	if byDepth0 != len(result.Roots) {
		t.Fatalf("depth=0 节点数 %d 与 roots 数 %d 不一致", byDepth0, len(result.Roots))
	}
}
