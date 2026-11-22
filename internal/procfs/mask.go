package procfs

import "strings"

// sensitiveKeys 是命令行里需要脱敏的参数键（去掉前导 '-' 后小写比较）。
// 只覆盖常见密钥参数，避免过度脱敏正常业务参数。
var sensitiveKeys = map[string]bool{
	"password":    true,
	"passwd":      true,
	"pwd":         true,
	"token":       true,
	"secret":      true,
	"api-key":     true,
	"apikey":      true,
	"access-key":  true,
	"secret-key":  true,
	"private-key": true,
}

// maskArgs 把形如 --password=xxx / --token=xxx 的参数值替换为 ***，并处理
// "--password secret" 这种以空格分隔的写法（脱敏紧随其后的值）。
// mask=false 时原样返回，不复制切片。
func maskArgs(args []string, mask bool) []string {
	if !mask || len(args) == 0 {
		return args
	}
	masked := make([]string, len(args))
	copy(masked, args)
	maskNext := false
	for i, arg := range masked {
		if maskNext {
			masked[i] = "***"
			maskNext = false
			continue
		}
		key, _, hasValue := strings.Cut(arg, "=")
		normalized := strings.ToLower(strings.TrimLeft(key, "-"))
		if !sensitiveKeys[normalized] {
			continue
		}
		if hasValue {
			masked[i] = key + "=***"
		} else {
			maskNext = true
		}
	}
	return masked
}

// maskCmdline 对已用单个空格拼接的命令行做脱敏；空串原样返回。
func maskCmdline(cmdline string, mask bool) string {
	if cmdline == "" || !mask {
		return cmdline
	}
	return strings.Join(maskArgs(strings.Split(cmdline, " "), true), " ")
}
