package scenarios

import (
	"embed"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/get-vix/vix/e2e/harness"
)

// scenarioSources embeds this package's test sources so the harness can capture
// each test function's body into its artifact (powering the report's
// collapsible implementation view) without shipping source to the container.
//
//go:embed *.go
var scenarioSources embed.FS

// TestMain runs the suite, then (when configured) invokes the standalone
// report binary to render the HTML + zip from the artifacts the harness
// appended. Rendering lives in a separate binary on purpose, so this test
// binary stays lean and free of the chroma/template dependencies.
func TestMain(m *testing.M) {
	harness.RegisterSources(scenarioSources)

	code := m.Run()

	// Single-container runs render + zip here. Sharded runs (SHARD_TOTAL>1)
	// leave their artifacts for the host to merge across shards, then render.
	root := os.Getenv("VIX_E2E_REPORT")
	bin := os.Getenv("REPORT_BIN")
	sharded := os.Getenv("SHARD_TOTAL") != "" && os.Getenv("SHARD_TOTAL") != "1"
	if root != "" && bin != "" && !sharded {
		_ = exec.Command(bin, "render", "--in", root).Run()
		_ = exec.Command(bin, "zip", "--in", root, "--out",
			filepath.Join(filepath.Dir(root), "e2e-report.zip")).Run()
	}
	os.Exit(code)
}
