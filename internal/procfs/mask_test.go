package procfs

import (
	"strings"
	"testing"
)

// TestMaskCmdline 覆盖敏感键脱敏、非敏感参数保留与空格分隔写法。
func TestMaskCmdline(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sleep 300 --password=secret", "sleep 300 --password=***"},
		{"--token=abc --name=x", "--token=*** --name=x"},
		{"--password secret", "--password ***"},
		{"PASSWORD=abc", "PASSWORD=***"},
		{"mysql --user=root --password=toor -h db", "mysql --user=root --password=*** -h db"},
		{"-p secret", "-p secret"},
		{"sleep 300", "sleep 300"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := maskCmdline(tc.in, true); got != tc.want {
			t.Fatalf("maskCmdline(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestMaskCmdlineDisabled 关闭脱敏时原样返回。
func TestMaskCmdlineDisabled(t *testing.T) {
	const raw = "sleep 300 --password=secret"
	if got := maskCmdline(raw, false); got != raw {
		t.Fatalf("mask=false 应原样返回，实际 %q", got)
	}
}

// TestMaskArgsNoLeak 断言脱敏后原始密钥值不出现在结果里。
func TestMaskArgsNoLeak(t *testing.T) {
	args := []string{"app", "--password=SuperSecret123", "--token=abc", "--api-key=k1", "--normal=ok"}
	masked := strings.Join(maskArgs(args, true), " ")
	for _, secret := range []string{"SuperSecret123", "abc", "k1"} {
		if strings.Contains(masked, secret) {
			t.Fatalf("脱敏结果仍包含密钥 %q: %s", secret, masked)
		}
	}
	if !strings.Contains(masked, "--normal=ok") {
		t.Fatalf("非敏感参数被误改: %s", masked)
	}
}
