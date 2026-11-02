package rewriter

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// shimSource stands in for the jscodeshift CLI in hermetic tests, so they need
// only Node. Like the real tool it require()s the transform module (any
// top-level code in the generated file runs at load time) and then invokes the
// exported function once, against a small stub of the jscodeshift API.
//
// Environment, all optional:
//
//	SHIM_ARGV_TO     write the argv the CLI received (JSON) to this path
//	SHIM_CWD_TO      write the working directory the CLI ran in to this path
//	SHIM_COPY_TO     copy the transform file to this path before loading it
//	SHIM_SCENARIO    JSON array of IfStatement nodes the stub AST contains
//	SHIM_RESULT_TO   write every replaceWith() argument (JSON) to this path
//	SHIM_DONE_TO     write a marker once the transform has loaded and run
const shimSource = `#!/usr/bin/env node
const fs = require('fs');
const args = process.argv.slice(2);
if (process.env.SHIM_ARGV_TO) { fs.writeFileSync(process.env.SHIM_ARGV_TO, JSON.stringify(args)); }
if (process.env.SHIM_CWD_TO) { fs.writeFileSync(process.env.SHIM_CWD_TO, process.cwd()); }
const at = args.indexOf('--transform');
if (at < 0 || !args[at + 1]) { console.error('shim: missing --transform'); process.exit(2); }
const transformPath = args[at + 1];
if (process.env.SHIM_COPY_TO) { fs.copyFileSync(transformPath, process.env.SHIM_COPY_TO); }
const transform = require(transformPath);
const scenario = process.env.SHIM_SCENARIO ? JSON.parse(process.env.SHIM_SCENARIO) : [];
const replaced = [];
function j(arg) {
  if (typeof arg === 'string') {
    return {
      find: function () {
        return { forEach: function (cb) { scenario.forEach(function (node) { cb({ node: node }); }); } };
      },
      toSource: function () { return 'ok'; },
    };
  }
  return { replaceWith: function (nodes) { replaced.push(nodes); } };
}
j.IfStatement = 'IfStatement';
j.blockStatement = function (body) { return { type: 'BlockStatement', body: body }; };
transform({ path: 'x.ts', source: 'src' }, { jscodeshift: j }, {});
if (process.env.SHIM_RESULT_TO) { fs.writeFileSync(process.env.SHIM_RESULT_TO, JSON.stringify(replaced)); }
if (process.env.SHIM_DONE_TO) { fs.writeFileSync(process.env.SHIM_DONE_TO, 'done'); }
`

// installShim puts the Node-based jscodeshift shim first on PATH for one test.
// Without Node the test skips locally but fails in CI, so a runner that loses
// Node cannot silently turn the injection regression tests off.
func installShim(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("node is not on PATH in CI: the injection regression tests need it")
		}
		t.Skip("node is not on PATH: the hermetic jscodeshift shim needs it")
	}
	installExecutable(t, shimSource)
}

// installExecutable makes src the "jscodeshift" executable found first on PATH
// for one test.
func installExecutable(t *testing.T, src string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, "jscodeshift")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("writing jscodeshift stand-in: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil { // it must be executable
		t.Fatalf("making jscodeshift stand-in executable: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// requirePayloadFires is the positive control for every sentinel test: it proves
// the payload really creates its sentinel when executed, so "no sentinel" can
// only mean "not executed" and never "the sentinel mechanism is broken".
func requirePayloadFires(t *testing.T) {
	t.Helper()
	sentinel := filepath.Join(t.TempDir(), "control")
	if out, err := exec.Command("node", "-e", payloadJS(t, sentinel)).CombinedOutput(); err != nil {
		t.Fatalf("control payload failed to run: %v\n%s", err, out)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("control payload did not create its sentinel (%v): sentinel tests would pass vacuously", err)
	}
}

// TestRewriteWithJscodeshift_HostileInputsNeverExecute is the regression test
// for the code-injection finding: no flag key or winning variant may ever run
// as code, whichever way it tries to break out of the generated transform. The
// transform must also have loaded and run, otherwise "nothing executed" proves
// nothing.
func TestRewriteWithJscodeshift_HostileInputsNeverExecute(t *testing.T) {
	installShim(t)
	requirePayloadFires(t)
	for _, tc := range hostileCases {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := filepath.Join(t.TempDir(), "pwned")
			done := filepath.Join(t.TempDir(), "done")
			t.Setenv("SHIM_DONE_TO", done)
			key, variant := tc.build(payloadJS(t, sentinel))

			res, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), key, variant, true)
			if err != nil {
				t.Fatalf("RewriteWithJscodeshift returned an error: %v", err)
			}
			if !res.TransformApplied {
				t.Fatalf("shim run failed, so the transform may never have loaded: %s\n%s", res.Notes, res.RawOutput)
			}
			if _, err := os.Stat(done); err != nil {
				t.Fatalf("the transform never finished running (%v): the check below would be vacuous", err)
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

// ---- JS-side wiring against a stub AST -----------------------------------------

func stringLiteral(v string) map[string]any {
	return map[string]any{"type": "StringLiteral", "value": v}
}

func memberCall(key string) map[string]any {
	return map[string]any{
		"type":      "CallExpression",
		"callee":    map[string]any{"type": "MemberExpression", "property": map[string]any{"name": "evaluate"}},
		"arguments": []any{stringLiteral(key)},
	}
}

func bareCall(key string) map[string]any {
	return map[string]any{
		"type":      "CallExpression",
		"callee":    map[string]any{"type": "Identifier", "name": "evaluate"},
		"arguments": []any{stringLiteral(key)},
	}
}

func block(marker string) map[string]any {
	return map[string]any{"type": "BlockStatement", "body": []any{marker}}
}

func ifNode(test, consequent, alternate map[string]any) map[string]any {
	node := map[string]any{"test": test, "consequent": consequent}
	if alternate != nil {
		node["alternate"] = alternate
	}
	return node
}

// TestTransform_StubASTWiring checks the JS side of the contract with nothing
// but Node: params.json is found next to the transform, the flag key and branch
// index are consumed under the right names, and each supported call shape
// collapses to the right branch. Real jscodeshift is not needed, so this runs in
// CI as well.
func TestTransform_StubASTWiring(t *testing.T) {
	installShim(t)

	stmt := map[string]any{"type": "ExpressionStatement", "marker": "STMT"}
	elseStmt := map[string]any{"type": "ExpressionStatement", "marker": "ELSE_STMT"}

	tests := []struct {
		name    string
		nodes   []map[string]any
		flagKey string
		variant string
		want    [][]any
	}{
		{"member call, enabled variant keeps the if branch",
			[]map[string]any{ifNode(memberCall("k"), block("IF"), block("ELSE"))}, "k", "true", [][]any{{"IF"}}},
		{"member call, disabled variant keeps the else branch",
			[]map[string]any{ifNode(memberCall("k"), block("IF"), block("ELSE"))}, "k", "false", [][]any{{"ELSE"}}},
		{"bare evaluate call matches",
			[]map[string]any{ifNode(bareCall("k"), block("IF"), block("ELSE"))}, "k", "on", [][]any{{"IF"}}},
		{"a different flag key is left alone",
			[]map[string]any{ifNode(memberCall("other"), block("IF"), block("ELSE"))}, "k", "true", [][]any{}},
		{"an else-less if with a disabled variant becomes an empty block",
			[]map[string]any{ifNode(memberCall("k"), block("IF"), nil)}, "k", "off", [][]any{{}}},
		{"a non-block branch is inlined as one statement",
			[]map[string]any{ifNode(memberCall("k"), stmt, elseStmt)}, "k", "enabled", [][]any{{stmt}}},
		{"a key containing a comment terminator matches as plain data",
			[]map[string]any{ifNode(memberCall("x */ y"), block("IF"), block("ELSE"))}, "x */ y", "true", [][]any{{"IF"}}},
		{"a condition that is not a call is ignored",
			[]map[string]any{ifNode(map[string]any{"type": "Identifier", "name": "flag"}, block("IF"), block("ELSE"))}, "k", "true", [][]any{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scenario, err := json.Marshal(tc.nodes)
			if err != nil {
				t.Fatalf("encoding scenario: %v", err)
			}
			result := filepath.Join(t.TempDir(), "result.json")
			t.Setenv("SHIM_SCENARIO", string(scenario))
			t.Setenv("SHIM_RESULT_TO", result)

			res, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), tc.flagKey, tc.variant, true)
			if err != nil {
				t.Fatalf("RewriteWithJscodeshift returned an error: %v", err)
			}
			if !res.TransformApplied {
				t.Fatalf("the transform did not load and run: %s\n%s", res.Notes, res.RawOutput)
			}

			raw, err := os.ReadFile(result)
			if err != nil {
				t.Fatalf("reading shim result: %v", err)
			}
			var got, want any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("shim result is not JSON: %v", err)
			}
			wantRaw, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatalf("encoding expectation: %v", err)
			}
			if err := json.Unmarshal(wantRaw, &want); err != nil {
				t.Fatalf("decoding expectation: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("replacements = %s, want %s", raw, wantRaw)
			}
		})
	}
}
