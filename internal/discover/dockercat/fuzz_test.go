package dockercat

import (
	"context"
	"os"
	"testing"
)

// FuzzDecodeCatalog feeds arbitrary bytes through decode and the mapping
// of every decoded entry: no panic, and every draft it hands out passes
// the add command (ToDraft's final gate).
func FuzzDecodeCatalog(f *testing.F) {
	for _, p := range []string{"testdata/catalog.yaml", "testdata/alias_bomb.yaml"} {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("registry:\n  a:\n    type: remote\n    remote: {transport_type: sse, url: 'https://a.example.com', headers: {A: 'Bearer ${T}'}}\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		cat, err := decode(context.Background(), raw)
		if err != nil {
			return
		}
		for _, it := range cat.items {
			e := toEntry(it.key, it.e)
			if len(e.Installs) != 1 {
				t.Fatalf("%q: %d installs", it.key, len(e.Installs))
			}
			if in := e.Installs[0]; (in.Draft == nil) == (in.Unsupported == "") {
				t.Fatalf("%q: draft %v, reason %q", it.key, in.Draft != nil, in.Unsupported)
			}
		}
	})
}
