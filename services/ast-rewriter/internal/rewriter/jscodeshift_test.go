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

// shimSource stands in for the jscodeshift CLI in hermetic tests. Like the real
// tool it require()s the transform module, so any top-level code in the
// generated file executes at load time, and then invokes the exported function
// once against a minimal stub of the jscodeshift API. SHIM_COPY_TO, when set,
// receives a copy of the transform file before it is loaded.
const shimSource = `#!/usr/bin/env node
const fs = require('fs');
const args = process.argv.slice(2);
const at = args.indexOf('--transform');
if (at < 0 || !args[at + 1]) { console.error('shim: missing --transform'); process.exit(2); }
const transformPath = args[at + 1];
if (process.env.SHIM_COPY_TO) { fs.copyFileSync(transformPath, process.env.SHIM_COPY_TO); }
const transform = require(transformPath);
const j = Object.assign(
  function () {
    return {
      find: function () { return { forEach: function () {} }; },
      toSource: function () { return ''; },
    };
  },
  {
    IfStatement: 'IfStatement',
    blockStatement: function (body) { return { type: 'BlockStatement', body: body }; },
  }
);
transform({ path: 'x.ts', source: '' }, { jscodeshift: j }, {});
`

// installShim puts the Node-based jscodeshift shim first on PATH for one test.
func installShim(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH: the hermetic jscodeshift shim needs it")
	}
	binDir := t.TempDir()
	shimPath := filepath.Join(binDir, "jscodeshift")
	if err := os.WriteFile(shimPath, []byte(shimSource), 0o600); err != nil {
		t.Fatalf("writing jscodeshift shim: %v", err)
	}
	if err := os.Chmod(shimPath, 0o700); err != nil { // the shim must be executable
		t.Fatalf("making jscodeshift shim executable: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

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

// TestRewriteWithJscodeshift_HostileInputsNeverExecute is the regression test
// for the code-injection finding: no flag key or winning variant may ever run
// as code, whichever way it tries to break out of the generated transform.
func TestRewriteWithJscodeshift_HostileInputsNeverExecute(t *testing.T) {
	installShim(t)
	for _, tc := range hostileCases {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := filepath.Join(t.TempDir(), "pwned")
			key, variant := tc.build(payloadJS(t, sentinel))

			if _, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), key, variant, true); err != nil {
				t.Fatalf("RewriteWithJscodeshift returned an error: %v", err)
			}
			assertNotExecuted(t, sentinel)
		})
	}
}

// TestRewriteWithJscodeshift_TransformSourceIgnoresInputs pins the property that
// makes the fix hold by construction: the generated transform program is the
// same bytes whatever the flag key and winning variant are, so no caller data
// can ever reach executable code.
func TestRewriteWithJscodeshift_TransformSourceIgnoresInputs(t *testing.T) {
	installShim(t)
	copyDir := t.TempDir()

	generate := func(name, key, variant string) string {
		t.Helper()
		dst := filepath.Join(copyDir, name)
		t.Setenv("SHIM_COPY_TO", dst)
		if _, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), key, variant, true); err != nil {
			t.Fatalf("%s: RewriteWithJscodeshift returned an error: %v", name, err)
		}
		body, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("%s: shim did not capture the transform: %v", name, err)
		}
		return string(body)
	}

	enabled := generate("enabled", "new-checkout", "true")
	disabled := generate("disabled", "other-flag", "false")
	hostile := generate("hostile", `x */ evil() /*`, `on */ evil() /*`)

	if enabled != disabled {
		t.Errorf("transform source depends on the flag key or variant (enabled vs disabled differ)")
	}
	if enabled != hostile {
		t.Errorf("transform source depends on the flag key or variant (benign vs hostile differ)")
	}
	if strings.Contains(hostile, "evil()") {
		t.Errorf("hostile input was embedded in the generated transform")
	}
}

// ---- real jscodeshift --------------------------------------------------------

func ifElseSource(t *testing.T, flagKey string) string {
	t.Helper()
	literal, err := json.Marshal(flagKey) // a double-quoted JS string literal
	if err != nil {
		t.Fatalf("quoting flag key: %v", err)
	}
	return "if (flagClient.evaluate(" + string(literal) + ", ctx)) {\n" +
		"  enabledPath();\n" +
		"} else {\n" +
		"  disabledPath();\n" +
		"}\n"
}

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

// TestTransformParams pins how caller values map onto the JSON parameters, and
// that hostile strings survive the JSON round trip byte for byte (they are data).
func TestTransformParams(t *testing.T) {
	branches := []struct {
		variant string
		want    int
	}{
		{"true", 0}, {"TRUE", 0}, {"on", 0}, {"ON", 0}, {"enabled", 0}, {"Enabled", 0},
		{"false", 1}, {"off", 1}, {"disabled", 1}, {"blue", 1}, {"", 1},
	}
	for _, tc := range branches {
		if got := newTransformParams("k", tc.variant).BranchIndex; got != tc.want {
			t.Errorf("variant %q: branchIndex = %d, want %d", tc.variant, got, tc.want)
		}
	}

	hostile := []string{`x */ evil() /*`, `x"; evil(); var y="`, "x\nevil()\n//", "x`;evil()//", "x evil()", `we"ird`, "flag-é-ü"}
	for _, key := range hostile {
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

// TestTransform_RealToolRewritesBranches proves legitimate behaviour: the
// matching flag collapses to the winning branch, and everything else is left
// alone, including keys with unusual characters.
func TestTransform_RealToolRewritesBranches(t *testing.T) {
	useRealJscodeshift(t)

	tests := []struct {
		name       string
		sourceFlag string // flag key present in the source file
		flagKey    string // flag key passed to the rewriter
		variant    string
		want       []string
		absent     []string
	}{
		{"enabled variant keeps the if branch", "new-checkout", "new-checkout", "true",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"on variant keeps the if branch", "new-checkout", "new-checkout", "ON",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"disabled variant keeps the else branch", "new-checkout", "new-checkout", "false",
			[]string{"disabledPath()"}, []string{"enabledPath()", "evaluate("}},
		{"a different flag is left untouched", "new-checkout", "other-flag", "true",
			[]string{"evaluate(", "enabledPath()", "disabledPath()"}, nil},
		{"namespaced key with punctuation matches", "team:feat.v2_beta-1", "team:feat.v2_beta-1", "true",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"key containing a double quote matches", `we"ird`, `we"ird`, "true",
			[]string{"enabledPath()"}, []string{"disabledPath()", "evaluate("}},
		{"non-ASCII key matches", "flag-é-ü", "flag-é-ü", "false",
			[]string{"disabledPath()"}, []string{"enabledPath()", "evaluate("}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			file := filepath.Join(repo, "a.ts")
			if err := os.WriteFile(file, []byte(ifElseSource(t, tc.sourceFlag)), 0o600); err != nil {
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
// worker only when there is at least one file to process.
func TestRewriteWithJscodeshift_RealToolHostileInputsNeverExecute(t *testing.T) {
	useRealJscodeshift(t)
	for _, tc := range hostileCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, "a.ts"), []byte(ifElseSource(t, "ok-flag")), 0o600); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			sentinel := filepath.Join(t.TempDir(), "pwned")
			key, variant := tc.build(payloadJS(t, sentinel))

			if _, err := RewriteWithJscodeshift(context.Background(), repo, key, variant, true); err != nil {
				t.Fatalf("RewriteWithJscodeshift returned an error: %v", err)
			}
			assertNotExecuted(t, sentinel)
		})
	}
}
