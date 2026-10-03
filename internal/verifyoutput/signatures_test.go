package verifyoutput

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSignatures(t *testing.T) {
	tests := []struct {
		name, output           string
		tests, packages, paths []string
	}{
		{name: "empty"},
		{name: "mixed", output: "--- FAIL: TestOne (0.00s)\n    --- FAIL: TestTwo/sub(0.01s)\nFAIL\texample.com/pkg\t0.003s\nFAIL\n", tests: []string{"TestOne", "TestTwo/sub"}, packages: []string{"example.com/pkg"}},
		{name: "build", output: "FAIL\tpkg [build failed]\nFAIL pkg/other [0.003s]\n", packages: []string{"pkg", "pkg/other"}},
		{name: "vet", output: "# pkg\ninternal/x/y.go:12:3: msg\ninternal/x/y.go:13: msg\n", paths: []string{"internal/x/y.go"}},
		{name: "unsafe paths", output: "/tmp/x.go:1:2: msg\n../x.go:1: msg\na/../../x.go:1: msg\nC:\\x.go:1: msg\n"},
		{name: "clean paths", output: "./internal/x/../y.go:1: msg\n", paths: []string{"internal/y.go"}},
		{name: "duplicates and CRLF", output: "--- FAIL: TestOne (0s)\r\n--- FAIL: TestOne (0s)\r\nFAIL pkg\r\nFAIL pkg\r\nx.go:1:2: msg\r\nx.go:2: msg\r\n", tests: []string{"TestOne"}, packages: []string{"pkg"}, paths: []string{"x.go"}},
		{name: "non failures", output: "ok pkg 0.01s\nFAILURE pkg\nx.go:bad: msg\nmessage x.go:1: msg\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, check := range []struct {
				name      string
				got, want []string
			}{{"tests", FailingTests(tt.output), tt.tests}, {"packages", FailingPackages(tt.output), tt.packages}, {"paths", FailingPaths(tt.output), tt.paths}} {
				if !reflect.DeepEqual(check.got, check.want) {
					t.Errorf("%s = %q, want %q", check.name, check.got, check.want)
				}
			}
		})
	}
}

func TestSignaturesBound(t *testing.T) {
	var output strings.Builder
	var tests, packages, paths []string
	for i := 0; i < 70; i++ {
		fmt.Fprintf(&output, "--- FAIL: Test%d (0s)\nFAIL pkg%d\nf%d.go:1: msg\n", i, i, i)
		if i < 64 {
			tests = append(tests, fmt.Sprintf("Test%d", i))
			packages = append(packages, fmt.Sprintf("pkg%d", i))
			paths = append(paths, fmt.Sprintf("f%d.go", i))
		}
	}
	if !reflect.DeepEqual(FailingTests(output.String()), tests) || !reflect.DeepEqual(FailingPackages(output.String()), packages) || !reflect.DeepEqual(FailingPaths(output.String()), paths) {
		t.Fatal("signatures did not retain the first 64 unique entries")
	}
}
