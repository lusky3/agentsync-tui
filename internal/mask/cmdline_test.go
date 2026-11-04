package mask

import (
	"reflect"
	"strings"
	"testing"
)

func TestCommandLine(t *testing.T) {
	c := "plainCANARY"
	tests := []struct{ in, want []string }{
		{[]string{"npx", "-y", "@upstash/context7-mcp", "--port", "8080", "-p", "3000"}, []string{"npx", "-y", "@upstash/context7-mcp", "--port", "8080", "-p", "3000"}},
		{[]string{"-e", "DB_PASS=" + c}, []string{"-e", "DB_PASS=" + Mask}},
		{[]string{"-e", "API_KEY=${env:A}"}, []string{"-e", "API_KEY=${env:A}"}},
		{[]string{"--mode=" + c}, []string{"--mode=" + Mask}},
		{[]string{"--pat", c, "--path", "/x"}, []string{"--pat", Mask, "--path", "/x"}},
		{[]string{"--private-key", c}, []string{"--private-key", Mask}},
		{[]string{"--token", "--verbose"}, []string{"--token", "--verbose"}},
		{[]string{"-H", "X-Custom: " + c}, []string{"-H", "X-Custom: " + Mask}},
	}
	for _, tt := range tests {
		if got := CommandLine(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("CommandLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFreeText(t *testing.T) {
	for _, in := range []string{
		"mytool --password plainCANARY", "mytool --api-key=plainCANARY", `mytool --password "plainCANARY`,
		`sh -c 'curl -H "Authorization: Bearer plainCANARY"'`, `x --token plainCANARY\`,
	} {
		if got := FreeText(in); strings.Contains(got, "CANARY") {
			t.Errorf("FreeText(%q) = %q", in, got)
		}
	}
	if got := FreeText(`npx -y "@scope/pkg"`); got != `npx -y "@scope/pkg"` {
		t.Errorf("benign input not echoed as typed: %q", got)
	}
}
