package procfs

import (
	"os/user"
	"strconv"
	"sync"
)

// userCache 把 uid 解析为用户名的结果缓存起来，避免同一份列表里重复查 /etc/passwd；
// 查不到的 uid 回退为数字字符串缓存，不做负数缓存或报错。
type userCache struct {
	mu    sync.Mutex
	names map[uint32]string
}

// newUserCache 构造空缓存。
func newUserCache() *userCache { return &userCache{names: make(map[uint32]string)} }

// lookup 返回 uid 对应的用户名；解析失败时返回 uid 的十进制字符串。
func (c *userCache) lookup(uid uint32) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name, ok := c.names[uid]; ok {
		return name
	}
	name := strconv.FormatUint(uint64(uid), 10)
	if entry, err := user.LookupId(name); err == nil && entry.Username != "" {
		name = entry.Username
	}
	c.names[uid] = name
	return name
}
