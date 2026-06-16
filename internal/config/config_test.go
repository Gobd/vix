package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeHomeSettings points HomeVixDir() at a temp home and writes settings.json.
func writeHomeSettings(t *testing.T, contents string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".vix")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if contents != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLogRetentionDays(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		want     int
	}{
		{"absent file", "", DefaultLogRetentionDays},
		{"absent key", `{"version":1}`, DefaultLogRetentionDays},
		{"explicit value", `{"logs":{"retention_days":3}}`, 3},
		{"explicit zero disables", `{"logs":{"retention_days":0}}`, 0},
		{"negative disables", `{"logs":{"retention_days":-1}}`, -1},
		{"unparsable falls back", `{not json`, DefaultLogRetentionDays},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeHomeSettings(t, tc.settings)
			if got := LogRetentionDays(); got != tc.want {
				t.Errorf("LogRetentionDays() = %d, want %d", got, tc.want)
			}
		})
	}
}
