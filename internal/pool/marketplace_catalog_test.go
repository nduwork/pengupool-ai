package pool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCatalogFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".pengupool"), 0o755)
	sha := strings.Repeat("a", 40)
	os.WriteFile(filepath.Join(dir, catalogFile), []byte(`{"agents":[
	 {"name":"a","group":"code","repo":"https://x/h.git","path":"agents/code/a","sha":"`+sha+`"},
	 {"name":"b","repo":"https://x/h.git","path":"../b","sha":"`+sha+`"}]}`), 0o644)
	m, err := readMarketplace(dir)
	if err != nil || len(m.Skills) != 1 || m.Skipped != 1 || m.Skills[0].Category != "code" || m.Skills[0].Path != "agents/code/a" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestLaunchHarness(t *testing.T) {
	p := &Pool{}
	if _, err := p.launch(context.Background(), launchIn{Harness: "foo"}); err == nil || !strings.Contains(err.Error(), "not \"foo\"") {
		t.Fatalf("foo: %v", err)
	}
	// shell passes the harness check (it then fails on the missing source, not on the harness)
	if _, err := p.launch(context.Background(), launchIn{Harness: "shell"}); err != nil && strings.Contains(err.Error(), "not \"shell\"") {
		t.Fatalf("shell refused: %v", err)
	}
}
