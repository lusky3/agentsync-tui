package model

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/testenv"
)

func TestParseSecretKeys(t *testing.T) {
	got, err := ParseSecretKeys(fixture(t, "secret-list.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"API_KEY", "GITHUB_TOKEN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseSecretKeysEmptyVault(t *testing.T) {
	got, err := ParseSecretKeys(fixture(t, "secret-list-empty.txt"))
	if err != nil || len(got) != 0 {
		t.Fatalf("empty vault: %q, %v", got, err)
	}
	got, err = ParseSecretKeys("")
	if err != nil || len(got) != 0 {
		t.Fatalf("blank: %q, %v", got, err)
	}
}

func TestFreshHomeFailureRecognised(t *testing.T) {
	msg := fixture(t, "secret-list-notconfigured.txt")
	if !IsBackendNotConfigured(msg) {
		t.Fatal("real fresh-home message not recognised")
	}
	if IsBackendNotConfigured("✗ ERROR  something else") {
		t.Fatal("false positive")
	}
	// the failure text is not a key list
	if _, err := ParseSecretKeys(strings.ReplaceAll(msg, "ERROR", "")); err == nil {
		// "✗" fails the key charset, so nothing is recognised
		t.Fatal("failure text parsed as keys")
	}
}

func TestParseSecretKeysTolerantOfJunk(t *testing.T) {
	// headings, notes and blank lines are skipped; multi-column lines are
	// prose (the CLI prints one key per line) and never yield a key
	in := "\n  Secrets:\n\nA_ONE   (updated 2026-01-01)\n\tB.two\t extra\t cols\n\tC_THREE\t\n(1 more)\n"
	got, err := ParseSecretKeys(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"C_THREE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if _, err := ParseSecretKeys("!!! ??? ###\n"); err == nil {
		t.Fatal("unrecognisable input must be an error")
	}
}

func TestParseSecretKeysKeepsHeaderLikeNames(t *testing.T) {
	in := "KEY\nNAME\nSECRET\nERROR\nINFO\nWARN\nKEYS\nSECRETS\nname\n"
	got, err := ParseSecretKeys(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Fields(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	got, err = ParseSecretKeys("SECRET\n")
	if err != nil || !reflect.DeepEqual(got, []string{"SECRET"}) {
		t.Fatalf("lone SECRET: %q, %v", got, err)
	}
}

func TestParseSecretKeysDropsHostileNames(t *testing.T) {
	in := "GOOD\n-rf\n../etc\n--flag=x\n\x1b[31mRED\nBI\u202eDI\nCTL\x07X\n.hidden\nsp ace\n" + strings.Repeat("A", 200) + "\nALSO_GOOD\n"
	got, err := ParseSecretKeys(in)
	if err != nil {
		t.Fatal(err)
	}
	// "sp ace" has two fields: prose, not the key "sp".
	if want := []string{"GOOD", "ALSO_GOOD"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseSecretKeysNeverReturnsValues(t *testing.T) {
	in := "GITHUB_TOKEN=CANARYx1\nAPI_KEY: CANARYx2\nOTHER_KEY CANARYx3\nQ=\"CANARYx4\"\n"
	got, err := ParseSecretKeys(in)
	if err != nil {
		t.Fatal(err)
	}
	// KEY=VALUE and Q="VALUE" are one field each and yield the key; the
	// two-field lines are prose and yield nothing. Either way no value leaks.
	if want := []string{"GITHUB_TOKEN", "Q"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if j := strings.Join(got, "\n"); strings.Contains(j, "CANARY") {
		t.Fatalf("value leaked: %q", j)
	}
}

// TestSecretListRealCLI runs the real agentsync in a hermetic home (fake
// values only) and checks the committed fixtures still match its output.
func TestSecretListRealCLI(t *testing.T) {
	keygen, err := exec.LookPath("age-keygen")
	if err != nil {
		t.Skip("age-keygen not on PATH")
	}
	e := testenv.New(t)
	e.Init(t)
	out, errOut, code := e.Agentsync(t, "--no-input", "--color", "never", "secret", "list")
	if code != 1 || out != "" || strings.TrimSpace(errOut) != strings.TrimSpace(fixture(t, "secret-list-notconfigured.txt")) {
		t.Fatalf("fresh home: exit %d out %q err %q", code, out, errOut)
	}
	keyFile := filepath.Join(e.Home, "age.key")
	if o, err := exec.Command(keygen, "-o", keyFile).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v %s", err, o)
	}
	pub := ""
	for _, l := range strings.Split(readFile(t, keyFile), "\n") {
		if strings.HasPrefix(l, "# public key: ") {
			pub = strings.TrimPrefix(l, "# public key: ")
		}
	}
	cfg := filepath.Join(e.AgentsyncHome, "agentsync.toml")
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n[secrets]\nbackend = \"age\"\nfile = \"secrets/secrets.age\"\nrecipient = \"" + pub + "\"\nidentity_file = \"" + keyFile + "\"\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	out, _, code = e.Agentsync(t, "--no-input", "--color", "never", "secret", "list")
	if code != 0 || out != fixture(t, "secret-list-empty.txt") {
		t.Fatalf("empty vault: exit %d out %q", code, out)
	}
	for _, k := range []string{"GITHUB_TOKEN", "API_KEY"} {
		if _, errOut, code := e.Agentsync(t, "--no-input", "--color", "never", "secret", "set", k+"=FAKEVALUE"); code != 0 {
			t.Fatalf("set %s: exit %d %s", k, code, errOut)
		}
	}
	out, _, code = e.Agentsync(t, "--no-input", "--color", "never", "secret", "list")
	if code != 0 || out != fixture(t, "secret-list.txt") {
		t.Fatalf("configured: exit %d out %q", code, out)
	}
	if keys, err := ParseSecretKeys(out); err != nil || len(keys) != 2 {
		t.Fatalf("parse real output: %q %v", keys, err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// agentsync prints exactly one key per line (secrets.go: Fprintln(w,
// Sanitize(k))), so a line with more than one field is prose, not a key:
// "No secrets stored" must not yield the key "No", and output made only of
// such lines is unreadable rather than empty.
func TestParseSecretKeysRejectsProse(t *testing.T) {
	if got, err := ParseSecretKeys("No secrets stored\n"); err == nil || len(got) != 0 {
		t.Fatalf("prose parsed as keys: %q, %v", got, err)
	}
	got, err := ParseSecretKeys("Name Description\nAPI_KEY\nkeys: 1 total\n")
	if err != nil || !reflect.DeepEqual(got, []string{"API_KEY"}) {
		t.Fatalf("got %q, %v", got, err)
	}
}
