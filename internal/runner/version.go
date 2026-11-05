package runner

import (
	"context"
	"strings"
)

// TestedVersion is the agentsync release this TUI is tested against.
const TestedVersion = "v0.17.0"

// CheckVersion runs `agentsync version` and reports the version token and
// whether it shares TestedVersion's minor. Output format:
// "agentsync <version> (commit <sha>, built <date>)". A non-semver token such
// as "dev" is returned as-is with supported == false.
func CheckVersion(ctx context.Context, r Runner) (v string, supported bool, err error) {
	res, err := r.Run(ctx, "version")
	if err != nil {
		return "", false, err
	}
	f := strings.Fields(res.Stdout)
	if len(f) < 2 {
		return "", false, nil
	}
	return f[1], sameMinor(f[1], TestedVersion), nil
}

// sameMinor compares "vMAJOR.MINOR" prefixes, ignoring patch.
func sameMinor(a, b string) bool {
	return minor(a) != "" && minor(a) == minor(b)
}

func minor(v string) string {
	p := strings.SplitN(v, ".", 3)
	if len(p) < 3 || !strings.HasPrefix(p[0], "v") {
		return ""
	}
	return p[0] + "." + p[1]
}
