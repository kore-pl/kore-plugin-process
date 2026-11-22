package procfs

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TreeNode 是进程树中的一个节点；depth 以被查询 pid 为 0。
type TreeNode struct {
	PID   int    `json:"pid"`
	PPID  int    `json:"ppid"`
	Name  string `json:"name"`
	State string `json:"state"`
	Depth int    `json:"depth"`
}

// treeResult 是 process.tree 结构；truncated 表示因深度或节点上限被截断。
type treeResult struct {
	Schema    string     `json:"schema"`
	Root      int        `json:"root"`
	Count     int        `json:"count"`
	Truncated bool       `json:"truncated"`
	MaxDepth  int        `json:"max_depth"`
	MaxNodes  int        `json:"max_nodes"`
	Nodes     []TreeNode `json:"nodes"`
}

// treeParams 是 process.tree 入参。
type treeParams struct {
	PID int `json:"pid"`
}

// queueItem 是 BFS 队列元素：待展开的 pid 与它自身的深度。
type queueItem struct {
	pid   int
	depth int
}

// callTree 从一次 /proc 扫描构建 ppid 反查表，BFS 输出以 pid 为根的子树。
// 深度 ≤10、节点 ≤500；命中上限时 truncated=true，不静默截断。
func (r *Runtime) callTree(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p treeParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if p.PID <= 0 {
		return nil, invalid("pid 必须为正整数")
	}
	records, err := r.src.Scan()
	if err != nil {
		return nil, classify(err)
	}
	byPID := make(map[int]Record, len(records))
	children := make(map[int][]int, len(records))
	for _, record := range records {
		byPID[record.PID] = record
		children[record.PPID] = append(children[record.PPID], record.PID)
	}
	root, ok := byPID[p.PID]
	if !ok {
		return nil, notFound(fmt.Sprintf("pid %d 不存在或已退出", p.PID))
	}
	for pid := range children {
		sort.Ints(children[pid])
	}
	result := treeResult{
		Schema: "process.tree.v1", Root: p.PID,
		MaxDepth: MaxTreeDepth, MaxNodes: MaxTreeNodes,
	}
	result.Nodes = append(result.Nodes, nodeOf(root, 0))
	queue := []queueItem{{pid: p.PID}}
	visited := map[int]bool{p.PID: true}
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		childPIDs := children[item.pid]
		if len(childPIDs) == 0 {
			continue
		}
		if item.depth >= MaxTreeDepth {
			result.Truncated = true
			continue
		}
		for _, childPID := range childPIDs {
			if visited[childPID] {
				continue
			}
			if len(result.Nodes) >= MaxTreeNodes {
				result.Truncated = true
				queue = nil
				break
			}
			visited[childPID] = true
			result.Nodes = append(result.Nodes, nodeOf(byPID[childPID], item.depth+1))
			queue = append(queue, queueItem{pid: childPID, depth: item.depth + 1})
		}
	}
	result.Count = len(result.Nodes)
	return encode(result)
}

// nodeOf 把采集记录转成树节点。
func nodeOf(record Record, depth int) TreeNode {
	return TreeNode{
		PID: record.PID, PPID: record.PPID, Name: record.Name,
		State: record.StateString(), Depth: depth,
	}
}
