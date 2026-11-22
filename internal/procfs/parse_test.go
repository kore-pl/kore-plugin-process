package procfs

import (
	"strings"
	"testing"
)

// TestParseStat 覆盖 comm 含空格与括号、字段索引与截断边界。
func TestParseStat(t *testing.T) {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[1] = "100"
	fields[11] = "12"
	fields[12] = "34"
	fields[19] = "999"
	data := "4242 (my proc (x)) " + strings.Join(fields, " ")
	info, err := parseStat(data)
	if err != nil {
		t.Fatalf("parseStat 失败: %v", err)
	}
	if info.Name != "my proc (x)" || info.State != 'S' || info.PPID != 100 {
		t.Fatalf("comm/state/ppid 解析不符: %+v", info)
	}
	if info.UTicks != 12 || info.STicks != 34 || info.StartTicks != 999 {
		t.Fatalf("ticks/starttime 解析不符: %+v", info)
	}
}

// TestParseStatInvalid 缺 comm 或字段不足时返回错误（不是零值冒充成功）。
func TestParseStatInvalid(t *testing.T) {
	if _, err := parseStat("no-comm-here"); err == nil {
		t.Fatal("缺少 comm 应报错")
	}
	if _, err := parseStat("1 (short) S 1"); err == nil {
		t.Fatal("字段不足应报错")
	}
}

// TestParseStatus 覆盖 Uid / State / VmRSS / VmPeak / Threads。
func TestParseStatus(t *testing.T) {
	data := strings.Join([]string{
		"Name:\tsleep",
		"State:\tS (sleeping)",
		"Uid:\t1000\t1000\t1000\t1000",
		"VmPeak:\t  5678 kB",
		"VmRSS:\t  1234 kB",
		"Threads:\t7",
	}, "\n")
	info := parseStatus(data)
	if !info.HasUID || info.UID != 1000 {
		t.Fatalf("uid 解析不符: %+v", info)
	}
	if info.State != 'S' || info.VmRSSKiB != 1234 || info.VmPeakKiB != 5678 || info.Threads != 7 {
		t.Fatalf("status 解析不符: %+v", info)
	}
}

// TestSplitCmdline 丢结尾空段并保留中间参数。
func TestSplitCmdline(t *testing.T) {
	parts := splitCmdline([]byte("sleep\x00300\x00--password=secret\x00\x00"))
	if len(parts) != 3 || parts[0] != "sleep" || parts[1] != "300" || parts[2] != "--password=secret" {
		t.Fatalf("cmdline 切分不符: %#v", parts)
	}
	if got := splitCmdline(nil); got != nil {
		t.Fatalf("空 cmdline 应返回 nil，实际 %#v", got)
	}
}

// TestParsePID 只接受纯数字正整数。
func TestParsePID(t *testing.T) {
	cases := map[string]bool{"1": true, "4242": true, "0": false, "-1": false, "12a": false, "": false, "self": false}
	for input, want := range cases {
		if _, ok := parsePID(input); ok != want {
			t.Fatalf("parsePID(%q) = %v，期望 %v", input, ok, want)
		}
	}
}
