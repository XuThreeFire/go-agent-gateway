package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDSNConversion(t *testing.T) {
	d := DatabaseConfig{URL: "mysql://user:p%40ss@127.0.0.1:13306/go_agent_gateway"}
	dsn, err := d.DSN()
	if err != nil {
		t.Fatalf("DSN 失败: %v", err)
	}
	if !strings.HasPrefix(dsn, "user:p@ss@tcp(127.0.0.1:13306)/go_agent_gateway?") {
		t.Fatalf("DSN 前半段不对: %s", dsn)
	}
	for _, want := range []string{"parseTime=true", "loc=UTC", "charset=utf8mb4"} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("DSN 缺少 %s: %s", want, dsn)
		}
	}
}

func TestDSNRejectsNonMySQL(t *testing.T) {
	if _, err := (DatabaseConfig{URL: "postgres://a:b@h:5432/db"}).DSN(); err == nil {
		t.Fatal("非 mysql:// 应当报错")
	}
	if _, err := (DatabaseConfig{URL: "mysql://user@127.0.0.1:3306"}).DSN(); err == nil {
		t.Fatal("缺少库名应当报错")
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeTempConfig(t, `
env: test
http:
  addr: "127.0.0.1:18080"
database:
  url: "mysql://u:p@127.0.0.1:3306/db"
llm:
  models:
    - name: m1
      provider: openai
      base_url: "http://127.0.0.1:9999/v1"
      upstream_model: m1
      enabled: true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if cfg.Hub.BatchSize != 32 {
		t.Fatalf("hub.batch_size 默认值 = %d", cfg.Hub.BatchSize)
	}
	if cfg.Hub.FlushInterval.Std() != 500*time.Millisecond {
		t.Fatalf("hub.flush_interval 默认值 = %s", cfg.Hub.FlushInterval.Std())
	}
	if cfg.HTTP.ReadHeaderTimeout.Std() != 10*time.Second {
		t.Fatalf("http.read_header_timeout 默认值 = %s", cfg.HTTP.ReadHeaderTimeout.Std())
	}
	if cfg.DefaultModel() != "m1" {
		t.Fatalf("默认模型 = %s", cfg.DefaultModel())
	}
}

func TestLoadRejectsEmptyModels(t *testing.T) {
	path := writeTempConfig(t, `
env: test
database:
  url: "mysql://u:p@127.0.0.1:3306/db"
`)
	if _, err := Load(path); err == nil {
		t.Fatal("没有模型时应当校验失败")
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	path := writeTempConfig(t, `
env: test
database:
  url: "mysql://u:p@127.0.0.1:3306/db"
  conn_max_lifetime: "半分钟"
llm:
  models:
    - name: m1
      provider: openai
      base_url: "http://x"
      upstream_model: m1
      enabled: true
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "非法时长") {
		t.Fatalf("期望非法时长错误，实际 %v", err)
	}
}

func TestResolveKeyEnvWins(t *testing.T) {
	m := Model{APIKey: "from-file", APIKeyEnv: "SAG_TEST_KEY"}
	if got := m.ResolveKey(); got != "from-file" {
		t.Fatalf("环境变量为空时应回落到文件值，实际 %s", got)
	}
	t.Setenv("SAG_TEST_KEY", "from-env")
	if got := m.ResolveKey(); got != "from-env" {
		t.Fatalf("环境变量应优先，实际 %s", got)
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return path
}

func TestRejectsPublicListenWithoutSecrets(t *testing.T) {
	path := writeTempConfig(t, `env: prod
http: {addr: ":8080"}
database: {url: "mysql://u:p@127.0.0.1:3306/db"}
llm:
  models:
    - {name: demo, provider: mock, upstream_model: demo, enabled: true}
`)
	t.Setenv("GATEWAY_SESSION_SECRET", "")
	t.Setenv("GATEWAY_ACCESS_PASSWORD", "")
	if _, err := Load(path); err == nil {
		t.Fatal("公网监听需要独立密钥和密码")
	}
}
func TestDatabaseURLFromEnvironment(t *testing.T) {
	path := writeTempConfig(t, `llm:
  models:
    - {name: demo, provider: mock, upstream_model: demo, enabled: true}
`)
	t.Setenv("DATABASE_URL", "mysql://u:p@127.0.0.1:3306/db")
	c, err := Load(path)
	if err != nil || c.Database.URL == "" {
		t.Fatalf("配置加载失败: %v", err)
	}
}

func TestPromptResolvePath(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		cfgPath  string
		expected string
	}{
		{"相对路径回退一级", "../prompts/system.md", "/srv/app/configs/dev/config.yaml", "/srv/app/configs/prompts/system.md"},
		{"相对路径同目录", "prompts/system.md", "/opt/go-agent-gateway/config.yaml", "/opt/go-agent-gateway/prompts/system.md"},
		{"绝对路径原样", "/etc/prompts/sys.md", "/opt/go-agent-gateway/config.yaml", "/etc/prompts/sys.md"},
		{"留空表示不启用", "", "/opt/go-agent-gateway/config.yaml", ""},
	}
	for _, c := range cases {
		got := PromptConfig{SystemFile: c.file}.ResolvePath(c.cfgPath)
		if got != c.expected {
			t.Fatalf("%s：ResolvePath = %q，期望 %q", c.name, got, c.expected)
		}
	}
}

// 全局日上限必须能区分三态：不写 = 默认 500 万，显式 0 = 不限，正数 = 该值。
// 如果只用一个 int64 让 0 同时表示「默认」和「不限」，线上写 0 想关掉的人会
// 拿到 500 万，想设上限的人会裸奔——两种误解都直接指向账单事故。
func TestGlobalDailyLimitTriState(t *testing.T) {
	const base = `
env: test
database:
  url: "mysql://u:p@127.0.0.1:3306/db"
llm:
  models:
    - name: m1
      provider: openai
      base_url: "http://x"
      upstream_model: m1
      enabled: true
`

	limitOf := func(t *testing.T, extra string) int64 {
		t.Helper()
		cfg, err := Load(writeTempConfig(t, base+extra))
		if err != nil {
			t.Fatalf("Load 失败: %v", err)
		}
		return cfg.Quota.GlobalDailyLimit()
	}

	if got := limitOf(t, ""); got != DefaultDailyTokensGlobal {
		t.Fatalf("未配置时 = %d，期望默认 %d", got, DefaultDailyTokensGlobal)
	}
	if DefaultDailyTokensGlobal != 5_000_000 {
		t.Fatalf("默认值应为 500 万，实际 %d", DefaultDailyTokensGlobal)
	}
	if got := limitOf(t, "\nquota:\n  daily_tokens_global: 0\n"); got != 0 {
		t.Fatalf("显式 0 = %d，期望 0（不限）", got)
	}
	if got := limitOf(t, "\nquota:\n  daily_tokens_global: 1234\n"); got != 1234 {
		t.Fatalf("显式正数 = %d，期望 1234", got)
	}

	// 负数没有语义，启动就该失败，而不是安静地当成「不限」。
	_, err := Load(writeTempConfig(t, base+"\nquota:\n  daily_tokens_global: -1\n"))
	if err == nil || !strings.Contains(err.Error(), "daily_tokens_global") {
		t.Fatalf("负数应当校验失败并点名字段，实际 %v", err)
	}
}

func TestMaxOutputTokensConfig(t *testing.T) {
	const base = `
database:
  url: "mysql://u:p@127.0.0.1:3306/db"
llm:
  models:
    - name: m
      enabled: true
      base_url: "http://localhost/v1"
      upstream_model: m
`
	for _, tc := range []struct {
		name    string
		yaml    string
		want    int
		invalid bool
	}{
		{name: "未配置", want: 2048},
		{name: "显式零", yaml: "executor:\n  max_output_tokens: 0\n", want: 2048},
		{name: "正数", yaml: "executor:\n  max_output_tokens: 123\n", want: 123},
		{name: "负数", yaml: "executor:\n  max_output_tokens: -1\n", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeTempConfig(t, base+tc.yaml))
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "executor.max_output_tokens") {
					t.Fatalf("负数应拒绝启动: %v", err)
				}
				return
			}
			if err != nil || cfg.Executor.MaxOutputTokens != tc.want {
				t.Fatalf("max_output_tokens=%d，期望%d，err=%v", cfg.Executor.MaxOutputTokens, tc.want, err)
			}
		})
	}
}
