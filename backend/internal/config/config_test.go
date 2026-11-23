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
