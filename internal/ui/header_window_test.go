package ui

import "testing"

// tabWindow is the pure core of the tab strip: it must always include the
// active label, grow right before left, count the ‹ / › markers, and report
// when even the active label does not fit.
func TestTabWindow(t *testing.T) {
	w := []int{5, 5, 5, 5, 5} // 25 wide in all
	cases := []struct {
		name          string
		active, avail int
		lo, hi        int
		ok            bool
	}{
		{"unlimited", 2, 0, 0, 4, true},
		{"fits whole", 2, 25, 0, 4, true},
		{"active only plus markers", 2, 7, 2, 2, true},
		{"grows right first", 1, 12, 1, 2, true}, // ‹ + 5 + 5 + › = 12
		{"grows both", 2, 17, 1, 3, true},        // ‹ + 15 + › = 17
		{"first tab needs no left marker", 0, 11, 0, 1, true},
		{"last tab needs no right marker", 4, 11, 3, 4, true},
		{"active does not fit", 2, 6, 2, 2, false}, // 5 + ‹ + › = 7 > 6
		{"active clamped high", 9, 25, 0, 4, true},
	}
	for _, c := range cases {
		lo, hi, ok := tabWindow(w, c.active, c.avail)
		if lo != c.lo || hi != c.hi || ok != c.ok {
			t.Errorf("%s: tabWindow = (%d, %d, %v), want (%d, %d, %v)", c.name, lo, hi, ok, c.lo, c.hi, c.ok)
		}
		if ok && (c.active < lo || c.active > hi) && c.active < len(w) {
			t.Errorf("%s: active %d outside [%d, %d]", c.name, c.active, lo, hi)
		}
	}
	if lo, hi, ok := tabWindow(nil, 0, 10); ok || lo != 0 || hi != -1 {
		t.Errorf("empty: (%d, %d, %v)", lo, hi, ok)
	}
}
