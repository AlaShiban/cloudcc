package deploy_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/cloudcompiler/cloudcc/internal/deploy"
)

// `cloudcc deploy` points a set of AWS services at the emulator, and the shell
// harness that drives pulumi directly has to point at the same set. Two lists,
// one fact.
//
// They drifted, and the failure was a long way from the cause: the differential
// suite deploys through `cloudcc deploy` and provisioned an RDS instance
// happily, while a harness configuring pulumi itself sent the availability zone
// lookup to real AWS and failed with "AuthFailure: AWS was not able to validate
// the provided access credentials". That reads as a credentials problem and is
// a missing endpoint.
func TestTheHarnessPointsAtTheSameServices(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "e2e", "lib.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("%s not found; this test runs from a checkout", path)
	}

	m := regexp.MustCompile(`(?s)CLOUDCC_E2E_SERVICES=\((.*?)\)`).FindStringSubmatch(string(data))
	if m == nil {
		t.Fatal("CLOUDCC_E2E_SERVICES is not declared where this test expects it")
	}
	var shell []string
	for _, field := range strings.Fields(strings.ReplaceAll(m[1], "\\\n", " ")) {
		if field != "\\" {
			shell = append(shell, field)
		}
	}

	want := append([]string(nil), deploy.EmulatedServices...)
	sort.Strings(want)
	sort.Strings(shell)

	if strings.Join(want, " ") != strings.Join(shell, " ") {
		t.Errorf("the two lists differ.\n  deploy.EmulatedServices: %v\n  tests/e2e/lib.sh:        %v\n"+
			"A service in one and not the other is a resource that provisions through "+
			"one path and reaches real AWS through the other.", want, shell)
	}
}
