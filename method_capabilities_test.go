package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// infoManifest 是 info.json 中本测试关心的字段（方法级能力声明）。
type infoManifest struct {
	ID                 string            `json:"id"`
	Version            string            `json:"version"`
	Capabilities       []string          `json:"capabilities"`
	MethodCapabilities map[string]string `json:"method_capabilities"`
}

// manifestHandshake 是随二进制内嵌的 manifest.json 握手字段子集。
type manifestHandshake struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// privilegedCapabilities 与内核唯一白名单逐项一致
// （kore internal/core/registry/validate.go：system:read|system:admin|net:read|
// net:write|proc:read|proc:write）。方法级能力只能是这 6 项之一。
var privilegedCapabilities = map[string]struct{}{
	"system:read": {}, "system:admin": {},
	"net:read": {}, "net:write": {},
	"proc:read": {}, "proc:write": {},
}

// readOnlyVerbs 是只读业务方法清单：必须声明 proc:read。
// 这张表是"只读 / 写"分类的验收依据——把只读方法误升为 proc:write 会让只读调用进写审计。
var readOnlyVerbs = map[string]struct{}{
	"list": {}, "detail": {}, "tree": {}, "ports": {},
}

// writeVerbs 是写业务方法清单：必须声明 proc:write（内核按 admin 级判定）。
var writeVerbs = map[string]struct{}{
	"kill": {}, "renice": {},
}

// loadInfoManifest 读取并解析仓根 info.json。
func loadInfoManifest(t *testing.T) infoManifest {
	t.Helper()
	raw, err := os.ReadFile("info.json")
	if err != nil {
		t.Fatalf("读取 info.json: %v", err)
	}
	var info infoManifest
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("解析 info.json: %v", err)
	}
	return info
}

// loadManifestHandshake 读取并解析仓根 manifest.json（随二进制内嵌的握手清单）。
func loadManifestHandshake(t *testing.T) manifestHandshake {
	t.Helper()
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("读取 manifest.json: %v", err)
	}
	var manifest manifestHandshake
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("解析 manifest.json: %v", err)
	}
	return manifest
}

// hasString 判断字符串切片是否包含目标值。
func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestMethodCapabilitiesMatchDispatch 是「声明 vs 实现」双向一致性门禁：
//   - info.json 声明的每条方法都必须命中 normalizeMethod（methods.go 的 businessVerbs）
//     且 Runtime.Call 有对应分支（错误不是"未知方法"）；
//   - businessVerbs 里的每个业务方法都必须被 info.json 声明；
//   - 只读 / 写分类锁定：readOnlyVerbs → proc:read，writeVerbs → proc:write；
//   - 方法级能力带内核认识的 6 项特权能力之一，且是插件级 capabilities 的子集；
//   - manifest.json 的 id / version / capabilities 与 info.json 同步。
func TestMethodCapabilitiesMatchDispatch(t *testing.T) {
	info := loadInfoManifest(t)
	if info.ID != "process" {
		t.Fatalf("info.json id=%q，期望 process", info.ID)
	}
	if len(info.MethodCapabilities) == 0 {
		t.Fatal("info.json 缺少 method_capabilities 声明")
	}

	// 声明 → 实现 + 分类 + 分发分支。
	for method, capability := range info.MethodCapabilities {
		if !strings.HasPrefix(method, info.ID+".") {
			t.Fatalf("方法键 %q 必须以插件 id %q 为域", method, info.ID)
		}
		if _, ok := privilegedCapabilities[capability]; !ok {
			t.Fatalf("%s 的能力 %q 不在内核 6 项特权白名单内", method, capability)
		}
		if !hasString(info.Capabilities, capability) {
			t.Fatalf("方法级能力 %q（%s）必须是插件级 capabilities %v 的子集",
				capability, method, info.Capabilities)
		}
		action := strings.TrimPrefix(method, info.ID+".")
		want := ""
		if _, ok := readOnlyVerbs[action]; ok {
			want = "proc:read"
		} else if _, ok := writeVerbs[action]; ok {
			want = "proc:write"
		} else {
			t.Fatalf("声明了 %q，但测试分类表未覆盖该动作（新增方法必须同步只读/写分类）", method)
		}
		if capability != want {
			t.Fatalf("%s 声明 %q，期望 %q（只读/写分级不可降级）", method, capability, want)
		}
		normalized, ok := normalizeMethod(method, info.ID)
		if !ok || normalized != method {
			t.Fatalf("%q 未命中 normalizeMethod（methods.go 的 businessVerbs 缺该动作）", method)
		}
		result, perr := newTestRuntime().Call(method, json.RawMessage(`{}`))
		if perr != nil && strings.Contains(perr.Message, "未知方法") {
			t.Fatalf("%s 在 Runtime.Call 没有分支: %+v", method, perr)
		}
		if perr == nil && len(result) == 0 {
			t.Fatalf("%s 分发返回空结果且无错误", method)
		}
	}

	// 实现 → 声明（反向覆盖）。
	for _, verb := range businessVerbs {
		if _, ok := info.MethodCapabilities[info.ID+"."+verb]; !ok {
			t.Fatalf("businessVerbs 实现了 %q，但 info.json 未声明方法级 capability", verb)
		}
		if _, ok := normalizeMethod(verb, info.ID); !ok {
			t.Fatalf("裸动作 %q 未被 normalizeMethod 接受", verb)
		}
	}
	if len(info.MethodCapabilities) != len(businessVerbs) {
		t.Fatalf("方法级声明 %d 条与业务方法 %d 个不一致", len(info.MethodCapabilities), len(businessVerbs))
	}

	// 分类表本身不得漂移：readOnlyVerbs + writeVerbs 必须恰好覆盖 businessVerbs。
	if got := len(readOnlyVerbs) + len(writeVerbs); got != len(businessVerbs) {
		t.Fatalf("只读/写分类表共 %d 条，businessVerbs 有 %d 个", got, len(businessVerbs))
	}
	for verb := range readOnlyVerbs {
		if !hasString(businessVerbs, verb) {
			t.Fatalf("分类表里的只读动作 %q 不在 businessVerbs 中", verb)
		}
	}
	for verb := range writeVerbs {
		if !hasString(businessVerbs, verb) {
			t.Fatalf("分类表里的写动作 %q 不在 businessVerbs 中", verb)
		}
	}
	if _, ok := normalizeMethod("process.exec", info.ID); ok {
		t.Fatal("未知方法 process.exec 不应被 normalizeMethod 接受")
	}

	// manifest.json 与 info.json 版本/身份/插件级能力同步。
	manifest := loadManifestHandshake(t)
	if manifest.ID != info.ID {
		t.Fatalf("manifest.id=%q 与 info.id=%q 不一致", manifest.ID, info.ID)
	}
	if manifest.Version != info.Version {
		t.Fatalf("manifest.version=%q 与 info.version=%q 不一致", manifest.Version, info.Version)
	}
	if len(manifest.Capabilities) != len(info.Capabilities) {
		t.Fatalf("manifest.capabilities=%v 与 info.capabilities=%v 不一致",
			manifest.Capabilities, info.Capabilities)
	}
	for i := range manifest.Capabilities {
		if manifest.Capabilities[i] != info.Capabilities[i] {
			t.Fatalf("manifest.capabilities=%v 与 info.capabilities=%v 不一致",
				manifest.Capabilities, info.Capabilities)
		}
	}
}
