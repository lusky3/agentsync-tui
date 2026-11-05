package shellwords

import (
	"reflect"
	"testing"
)

func TestShellWordsSplitter(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{in: "", want: []string{}},
		{in: "   \t ", want: []string{}},
		{in: "a b  c", want: []string{"a", "b", "c"}},
		{in: "-y @upstash/context7-mcp", want: []string{"-y", "@upstash/context7-mcp"}},
		{in: `--header "Authorization: Bearer x"`, want: []string{"--header", "Authorization: Bearer x"}},
		{in: `'single $quoted' x`, want: []string{"single $quoted", "x"}},
		{in: `a\ b c`, want: []string{"a b", "c"}},
		{in: `"a \"q\" b"`, want: []string{`a "q" b`}},
		{in: `'it\'`, want: []string{`it\`}},
		{in: `x"y z"w`, want: []string{"xy zw"}},
		{in: `"" ''`, want: []string{"", ""}},
		{in: "code -w", want: []string{"code", "-w"}},
		{in: `"unterminated`, wantErr: true},
		{in: `'unterminated`, wantErr: true},
		{in: `trailing\`, wantErr: true},
	}
	for _, tt := range tests {
		got, err := Split(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("Split(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Split(%q) error %v", tt.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Split(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
