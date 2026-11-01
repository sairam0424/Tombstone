package rewriter

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// useRealJscodeshift makes the real jscodeshift CLI resolvable for one test, or
// skips with instructions when it is not installed.
func useRealJscodeshift(t *testing.T) {
	t.Helper()
	if dir := os.Getenv("TOMBSTONE_TEST_JSCODESHIFT_DIR"); dir != "" {
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if _, err := exec.LookPath("jscodeshift"); err != nil {
		t.Skip("real jscodeshift not found: run `npm i -g jscodeshift`, or set " +
			"TOMBSTONE_TEST_JSCODESHIFT_DIR to a node_modules/.bin directory that contains it")
	}
}

// payloadJS returns JavaScript that proves execution by writing a sentinel file.
func payloadJS(t *testing.T, sentinel string) string {
	t.Helper()
	quoted, err := json.Marshal(sentinel)
	if err != nil {
		t.Fatalf("quoting sentinel path: %v", err)
	}
	return `require("fs").writeFileSync(` + string(quoted) + `,"pwn");`
}

type hostileCase struct {
	name  string
	build func(js string) (flagKey, winningVariant string)
}

// hostileCases are inputs that try to turn the flag key or the winning variant
// into executable JavaScript in the generated transform.
var hostileCases = []hostileCase{
	{"block comment breakout via flag key", func(js string) (string, string) {
		return "x */ " + js + " /*", "true"
	}},
	{"block comment breakout via winning variant", func(js string) (string, string) {
		return "ok-flag", "true */ " + js + " /*"
	}},
	{"double quote breakout via flag key", func(js string) (string, string) {
		return `x"; ` + js + ` var y="`, "true"
	}},
	{"newline then statement via flag key", func(js string) (string, string) {
		return "x\n" + js + "\n//", "true"
	}},
	{"backtick breakout via flag key", func(js string) (string, string) {
		return "x`; " + js + " //", "true"
	}},
	{"unicode line separator via flag key", func(js string) (string, string) {
		return "x\u2028" + js + "\u2028//", "true"
	}},
}

func assertNotExecuted(t *testing.T, sentinel string) {
	t.Helper()
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("injected JavaScript executed: sentinel file %s was created", sentinel)
	}
}

// controlKeys are flag keys that Go's %q escapes into sequences JSON does not
// accept (\a, \x00, \x7f, \U000e0001). They guard against ever serialising the
// parameters with %q instead of encoding/json.
var controlKeys = []string{"x\u0007y", "x\x00y", "x\x7fy", "x\U000e0001y"}

// TestWriteTransform_SourceIsConstant needs no Node: whatever the caller
// supplies, the transform file written to disk is exactly the compile-time
// constant, so request data cannot be part of it.
func TestWriteTransform_SourceIsConstant(t *testing.T) {
	inputs := []struct{ key, variant string }{
		{"new-checkout", "true"},
		{"other-flag", "false"},
		{"x */ evil() /*", "on */ evil() /*"},
		{`x"; evil(); var y="`, "blue"},
		{"x\nevil()\n//", "true"},
		{controlKeys[0], "enabled"},
	}
	for _, in := range inputs {
		path, err := writeTransform(t.TempDir(), in.key, in.variant)
		if err != nil {
			t.Fatalf("writeTransform(%q, %q): %v", in.key, in.variant, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading transform: %v", err)
		}
		if string(got) != transformSource {
			t.Errorf("transform for key %q variant %q differs from the constant source", in.key, in.variant)
		}
	}
}

// TestTransformParams pins how caller values map onto the JSON parameters, and
// that hostile strings survive the JSON round trip byte for byte (they are data).
func TestTransformParams(t *testing.T) {
	branches := []struct {
		variant string
		want    int
	}{
		{"true", 0}, {"TRUE", 0}, {"on", 0}, {"ON", 0}, {"enabled", 0}, {"Enabled", 0},
		{"false", 1}, {"off", 1}, {"disabled", 1}, {"blue", 1}, {"", 1},
		{"yes", 1}, {"1", 1}, {"y", 1}, {"t", 1}, // only true, on and enabled keep the if branch
	}
	for _, tc := range branches {
		if got := newTransformParams("k", tc.variant).BranchIndex; got != tc.want {
			t.Errorf("variant %q: branchIndex = %d, want %d", tc.variant, got, tc.want)
		}
	}

	// Keys must reach the transform verbatim: no case folding, no trimming.
	verbatim := []string{"MixedCase", "UPPER", "  padded  ", "\tTabbed\n"}
	hostile := append([]string{`x */ evil() /*`, `x"; evil(); var y="`, "x\nevil()\n//", "x`;evil()//", "x evil()", `we"ird`, "flag-é-ü"}, verbatim...)
	for _, key := range append(hostile, controlKeys...) {
		dir := t.TempDir()
		if _, err := writeTransform(dir, key, "true"); err != nil {
			t.Fatalf("writeTransform(%q): %v", key, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, paramsFileName))
		if err != nil {
			t.Fatalf("reading params for %q: %v", key, err)
		}
		var got transformParams
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("params for %q are not valid JSON: %v", key, err)
		}
		if got.FlagKey != key {
			t.Errorf("flag key did not survive the round trip: got %q, want %q", got.FlagKey, key)
		}
	}
}

// ---- real jscodeshift --------------------------------------------------------

// runRealTransform applies the generated transform to one file with the real
// jscodeshift CLI, selecting the tsx parser explicitly. The transform matches
// Babel-AST StringLiteral nodes, which the tsx, ts and babylon parsers emit;
// jscodeshift's default parser emits ESTree Literal nodes instead, so
// RewriteWithJscodeshift (which passes no --parser) does not rewrite under it.
// That limitation predates this change and is deliberately out of scope here.
func runRealTransform(t *testing.T, file, flagKey, variant string) {
	t.Helper()
	transformPath, err := writeTransform(t.TempDir(), flagKey, variant)
	if err != nil {
		t.Fatalf("writeTransform: %v", err)
	}
	out, err := exec.Command("jscodeshift", "--parser=tsx", "--silent", "--transform", transformPath, file).CombinedOutput()
	if err != nil {
		t.Fatalf("jscodeshift failed: %v\n%s", err, out)
	}
}

// TestTransform_RealToolRewritesBranches proves legitimate behaviour end to end:
// the matching flag collapses to the winning branch and everything else is left
// alone, for every supported call shape and for keys with unusual characters.
func TestTransform_RealToolRewritesBranches(t *testing.T) {
	useRealJscodeshift(t)

	literal := func(s string) string {
		b, err := json.Marshal(s) // a double-quoted JS string literal
		if err != nil {
			t.Fatalf("quoting %q: %v", s, err)
		}
		return string(b)
	}
	ifElse := func(call string) string {
		return "if (" + call + ") {\n  enabledPath();\n} else {\n  disabledPath();\n}\n"
	}
	member := func(key string) string { return "flagClient.evaluate(" + literal(key) + ", ctx)" }

	tests := []struct {
		name    string
		source  string
		flagKey string
		variant string
		want    []string
		absent  []string
	}{
		{"enabled variant keeps the if branch", ifElse(member("new-checkout")), "new-checkout", "true",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"on variant keeps the if branch", ifElse(member("new-checkout")), "new-checkout", "ON",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"disabled variant keeps the else branch", ifElse(member("new-checkout")), "new-checkout", "false",
			[]string{"disabledPath()"}, []string{"enabledPath()", "evaluate("}},
		{"a different flag is left untouched", ifElse(member("new-checkout")), "other-flag", "true",
			[]string{"evaluate(", "enabledPath()", "disabledPath()"}, nil},
		{"bare evaluate call matches", ifElse("evaluate(" + literal("new-checkout") + ")"), "new-checkout", "true",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"else-less if with a disabled variant vanishes", "if (" + member("k") + ") {\n  onlyIf();\n}\nafter();\n", "k", "false",
			[]string{"after()"}, []string{"onlyIf()", "evaluate("}},
		{"non-block branch is inlined", "if (c.evaluate(" + literal("k") + ")) a(); else b();\n", "k", "true",
			[]string{"a();"}, []string{"b();", "evaluate("}},
		{"namespaced key with punctuation matches", ifElse(member("team:feat.v2_beta-1")), "team:feat.v2_beta-1", "true",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"key containing a double quote matches", ifElse(member(`we"ird`)), `we"ird`, "true",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"non-ASCII key matches", ifElse(member("flag-é-ü")), "flag-é-ü", "false",
			[]string{"disabledPath()"}, []string{"enabledPath()", "evaluate("}},
		{"key with a BEL control character matches", ifElse(member(controlKeys[0])), controlKeys[0], "true",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"key with a non-printable astral rune matches", ifElse(member(controlKeys[3])), controlKeys[3], "true",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "a.ts")
			if err := os.WriteFile(file, []byte(tc.source), 0o600); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}

			runRealTransform(t, file, tc.flagKey, tc.variant)

			got, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("reading rewritten file: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(got), w) {
					t.Errorf("rewritten file is missing %q:\n%s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(string(got), a) {
					t.Errorf("rewritten file still contains %q:\n%s", a, got)
				}
			}
		})
	}
}

// TestRewriteWithJscodeshift_RealToolHostileInputsNeverExecute repeats the
// injection regression against the real CLI, which loads the transform inside a
// worker only when there is at least one file to process. "0 errors" in the
// tool's own report is the positive control that the worker really loaded the
// transform and processed the file, so a timeout or crash cannot pass this test.
func TestRewriteWithJscodeshift_RealToolHostileInputsNeverExecute(t *testing.T) {
	useRealJscodeshift(t)
	requirePayloadFires(t)
	for _, tc := range hostileCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			fixture := "if (flagClient.evaluate(\"ok-flag\", ctx)) {\n  enabledPath();\n} else {\n  disabledPath();\n}\n"
			if err := os.WriteFile(filepath.Join(repo, "a.ts"), []byte(fixture), 0o600); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			sentinel := filepath.Join(t.TempDir(), "pwned")
			key, variant := tc.build(payloadJS(t, sentinel))

			res, err := RewriteWithJscodeshift(context.Background(), repo, key, variant, true)
			if err != nil {
				t.Fatalf("RewriteWithJscodeshift returned an error: %v", err)
			}
			if !res.TransformApplied || !strings.Contains(res.RawOutput, "0 errors") {
				t.Fatalf("jscodeshift did not load the transform and process the file cleanly:\n%s", res.RawOutput)
			}
			assertNotExecuted(t, sentinel)
		})
	}
}
