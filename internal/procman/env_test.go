package procman

import "testing"

// TestClassifyEnv 验证关键环境变量的来源判定优先级：文件配置(非空) > 系统环境变量(非空) > 缺失。
// 用专用测试 key，并通过 t.Setenv 隔离系统环境，避免污染真实 ANTHROPIC_* 变量。
func TestClassifyEnv(t *testing.T) {
	const key = "LINKCODE_TEST_ENV_VAR"

	tests := []struct {
		name    string
		fileEnv map[string]string
		sysVal  string // 系统环境变量值；"" 视为未设置
		want    envSource
	}{
		{name: "文件优先（系统也有）", fileEnv: map[string]string{key: "fromfile"}, sysVal: "fromsys", want: envFromFile},
		{name: "仅文件", fileEnv: map[string]string{key: "fromfile"}, sysVal: "", want: envFromFile},
		{name: "系统兜底", fileEnv: nil, sysVal: "fromsys", want: envFromSystem},
		{name: "都缺失", fileEnv: nil, sysVal: "", want: envMissing},
		{name: "文件空值回退系统", fileEnv: map[string]string{key: ""}, sysVal: "fromsys", want: envFromSystem},
		{name: "文件空值且系统也空", fileEnv: map[string]string{key: ""}, sysVal: "", want: envMissing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(key, tt.sysVal)
			if got := classifyEnv(key, tt.fileEnv); got != tt.want {
				t.Errorf("classifyEnv(%q, %v) = %v, want %v", key, tt.fileEnv, got, tt.want)
			}
		})
	}
}
