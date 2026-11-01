package handlers

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoNetworkImports ensures metakill's own packages never import network
// packages. (pdfcpu links net/http for features metakill never calls, and
// metakill runs pdfcpu with Offline set.)
func TestNoNetworkImports(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}}: {{join .Imports " "}}`,
		"github.com/computerguymackay/metakill/...").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		for _, imp := range strings.Fields(line) {
			if imp == "net" || strings.HasPrefix(imp, "net/") {
				t.Errorf("network import: %s", line)
			}
		}
	}
}
