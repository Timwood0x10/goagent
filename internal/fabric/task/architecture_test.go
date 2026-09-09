package taskfabric

// Architecture red line (RUNTIME.md §8-A2): the task fabric CORE — this
// package (taskfabric top level), internal/fabric/agent, and
// internal/fabric/planprojection — must never import internal/runtime. The
// fabric is the engine the kernel schedules against; runtime is the service
// layer assembled on top. A reverse import would make the engine depend on
// the service graph it is supposed to be scheduled BY.
//
// The workflow/ subtree is deliberately outside this gate: it is the
// evolution-patch application surface and its dependency on
// runtime/evolution/patch types is an established, reviewed seam (the DAG
// patchers consume the patch vocabulary; they do not reach the runtime
// service graph). If that seam ever needs to move, this gate's scope note
// is the place to record the decision — not a silent import.
//
// Same mechanism as kernel's TestSchedulerMustNotImportRuntime: source-scan
// the package dirs, fail on the banned prefix. Non-Go files and tests are
// skipped (tests may freely construct runtime fixtures).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFabricCoreMustNotImportRuntime(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	// thisFile = internal/fabric/task/architecture_test.go → the fabric
	// root is two levels up.
	fabricDir := filepath.Dir(filepath.Dir(thisFile))

	// The fabric CORE packages under the gate. workflow/ is intentionally
	// absent (see the file comment).
	gated := []string{
		filepath.Join(fabricDir, "task"),
		filepath.Join(fabricDir, "agent"),
		filepath.Join(fabricDir, "planprojection"),
	}
	const banned = "github.com/Timwood0x10/ares/internal/runtime"

	for _, dir := range gated {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if strings.Contains(line, banned) {
					t.Fatalf("%s/%s:%d: fabric core must not import internal/runtime (the fabric is the engine the runtime schedules; a reverse import inverts the layering. If you need a runtime type here, define the interface at this consumer and adapt it in the cmd/runtime layer instead)",
						filepath.Base(dir), name, i+1)
				}
			}
		}
	}
}
