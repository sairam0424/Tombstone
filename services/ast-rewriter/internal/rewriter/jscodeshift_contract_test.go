package rewriter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The tests in this file pin the contract of RewriteWithJscodeshift and of the
// transform it generates: what the transform may and may not rewrite, what
// jscodeshift gets back, the exact command line and process environment, and
// how the tool's output becomes a result. The package had no tests before the
// injection fix, so none of it was guarded.

// ---- helpers ------------------------------------------------------------------

func callWith(callee map[string]any, args ...any) map[string]any {
	if args == nil {
		args = []any{}
	}
	return map[string]any{"type": "CallExpression", "callee": callee, "arguments": args}
}

func member(name string) map[string]any {
	return map[string]any{"type": "MemberExpression", "property": map[string]any{"name": name}}
}

func ident(name string) map[string]any { return map[string]any{"type": "Identifier", "name": name} }

// runStub runs the generated transform over a stub AST and returns how many
// replaceWith() calls it made.
func runStub(t *testing.T, nodes []map[string]any, key, variant string) int {
	t.Helper()
	installShim(t)
	scenario, err := json.Marshal(nodes)
	if err != nil {
		t.Fatalf("encoding scenario: %v", err)
	}
	result := filepath.Join(t.TempDir(), "result.json")
	t.Setenv("SHIM_SCENARIO", string(scenario))
	t.Setenv("SHIM_RESULT_TO", result)
	res, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), key, variant, true)
	if err != nil {
		t.Fatalf("RewriteWithJscodeshift returned an error: %v", err)
	}
	if !res.TransformApplied {
		t.Fatalf("the transform crashed or did not load: %s\n%s", res.Notes, res.RawOutput)
	}
	raw, err := os.ReadFile(result)
	if err != nil {
		t.Fatalf("reading shim result: %v", err)
	}
	var got []any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("shim result is not JSON: %v", err)
	}
	return len(got)
}

// ---- JS matching: only an exact evaluate("<key>") call may be rewritten --------

// A wrong match deletes live code, so near misses matter as much as hits.
func TestTransform_OnlyExactEvaluateCallsAreRewritten(t *testing.T) {
	tests := []struct {
		name string
		node map[string]any
		key  string
		want int
	}{
		{"control: member call with extra args",
			ifNode(callWith(member("evaluate"), stringLiteral("k"), ident("ctx")), block("I"), block("E")), "k", 1},
		{"key differs only by case",
			ifNode(callWith(member("evaluate"), stringLiteral("K")), block("I"), block("E")), "k", 0},
		{"call key has the flag key as a prefix",
			ifNode(callWith(member("evaluate"), stringLiteral("k2")), block("I"), block("E")), "k", 0},
		{"call key has the flag key as a suffix",
			ifNode(callWith(member("evaluate"), stringLiteral("xk")), block("I"), block("E")), "k", 0},
		{"call key has surrounding space",
			ifNode(callWith(member("evaluate"), stringLiteral(" k")), block("I"), block("E")), "k", 0},
		{"flag key has surrounding space",
			ifNode(callWith(member("evaluate"), stringLiteral("k")), block("I"), block("E")), " k", 0},
		{"other method name on a member call",
			ifNode(callWith(member("isEnabled"), stringLiteral("k")), block("I"), block("E")), "k", 0},
		{"other bare function name",
			ifNode(callWith(ident("check"), stringLiteral("k")), block("I"), block("E")), "k", 0},
		{"first argument is a Literal, not a StringLiteral",
			ifNode(callWith(member("evaluate"), map[string]any{"type": "Literal", "value": "k"}), block("I"), block("E")), "k", 0},
		{"member call without arguments",
			ifNode(callWith(member("evaluate")), block("I"), block("E")), "k", 0},
		{"bare call without arguments",
			ifNode(callWith(ident("evaluate")), block("I"), block("E")), "k", 0},
		{"negated check is not rewritten as if it were not negated",
			ifNode(map[string]any{"type": "UnaryExpression", "operator": "!", "argument": callWith(member("evaluate"), stringLiteral("k"))},
				block("I"), block("E")), "k", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := runStub(t, []map[string]any{tc.node}, tc.key, "true"); got != tc.want {
				t.Fatalf("replacements = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestTransform_EveryMatchingIfIsRewritten(t *testing.T) {
	match := func() map[string]any {
		return ifNode(callWith(member("evaluate"), stringLiteral("k")), block("I"), block("E"))
	}
	other := ifNode(callWith(member("evaluate"), stringLiteral("z")), block("I"), block("E"))
	if got := runStub(t, []map[string]any{match(), other, match()}, "k", "true"); got != 2 {
		t.Fatalf("replacements = %d, want 2 (one per matching if)", got)
	}
}

// TestTransform_ReturnsPrintedSource pins what jscodeshift receives back: the
// printed source (null would mean "unmodified"), printed with single quotes.
func TestTransform_ReturnsPrintedSource(t *testing.T) {
	src := shimSource
	for _, rep := range [][2]string{
		{"const replaced = [];", "const replaced = [];\nlet toSourceOpts = null;"},
		{"toSource: function () { return 'ok'; },", "toSource: function (o) { toSourceOpts = o; return 'ok'; },"},
		{"transform({ path: 'x.ts', source: 'src' }, { jscodeshift: j }, {});",
			"const ret = transform({ path: 'x.ts', source: 'src' }, { jscodeshift: j }, {});\n" +
				"fs.writeFileSync(process.env.SHIM_RET_TO, JSON.stringify({ ret: ret, opts: toSourceOpts }));"},
	} {
		if !strings.Contains(src, rep[0]) {
			t.Fatalf("shim no longer contains %q", rep[0])
		}
		src = strings.Replace(src, rep[0], rep[1], 1)
	}
	installExecutable(t, src)
	out := filepath.Join(t.TempDir(), "ret.json")
	t.Setenv("SHIM_RET_TO", out)
	if res, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), "k", "true", true); err != nil || !res.TransformApplied {
		t.Fatalf("run failed: %v %+v", err, res)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading shim output: %v", err)
	}
	var got struct {
		Ret  any            `json:"ret"`
		Opts map[string]any `json:"opts"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("shim output is not JSON: %v", err)
	}
	if got.Ret != "ok" {
		t.Errorf("transform returned %v, want the printed source", got.Ret)
	}
	if got.Opts["quote"] != "single" {
		t.Errorf("toSource options = %v, want quote=single", got.Opts)
	}
}

// ---- Go side: result mapping ---------------------------------------------------

func TestRewriteWithJscodeshift_ResultFollowsToolOutputAndExit(t *testing.T) {
	run := func(t *testing.T, script string, dryRun bool) *JSCodeshiftResult {
		t.Helper()
		installExecutable(t, "#!/bin/sh\n"+script)
		res, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), "k", "true", dryRun)
		if err != nil {
			t.Fatalf("RewriteWithJscodeshift returned an error: %v", err)
		}
		return res
	}

	t.Run("MOD lines from stdout and stderr are parsed; dry-run is noted", func(t *testing.T) {
		res := run(t, "echo ' MOD /r/a.ts'\necho 'NOC /r/d.ts'\necho ' MOD /r/c.ts' >&2\nexit 0\n", true)
		if !res.TransformApplied {
			t.Errorf("TransformApplied = false, want true: %s", res.Notes)
		}
		if strings.Join(res.FilesModified, ",") != "/r/a.ts,/r/c.ts" {
			t.Errorf("FilesModified = %v", res.FilesModified)
		}
		if res.LinesRemoved != 2 {
			t.Errorf("LinesRemoved = %d, want 2", res.LinesRemoved)
		}
		if !strings.Contains(res.Notes, "dry-run") {
			t.Errorf("dry-run not mentioned in Notes: %q", res.Notes)
		}
		if !strings.Contains(res.RawOutput, "NOC /r/d.ts") {
			t.Errorf("RawOutput lost tool output: %q", res.RawOutput)
		}
	})
	t.Run("a real run does not claim dry-run", func(t *testing.T) {
		res := run(t, "echo ' MOD /r/a.ts'\n", false)
		if strings.Contains(res.Notes, "dry-run") {
			t.Errorf("Notes mention dry-run on a real run: %q", res.Notes)
		}
	})
	t.Run("no MOD lines yields an empty, non-nil list", func(t *testing.T) {
		res := run(t, "exit 0\n", true)
		if !res.TransformApplied || res.FilesModified == nil || len(res.FilesModified) != 0 {
			t.Errorf("got applied=%v files=%#v", res.TransformApplied, res.FilesModified)
		}
	})
	t.Run("non-zero exit is not applied even when files were modified", func(t *testing.T) {
		res := run(t, "echo ' MOD /r/a.ts'\nexit 1\n", true)
		if res.TransformApplied {
			t.Errorf("TransformApplied = true after exit 1")
		}
		if !strings.Contains(res.Notes, "exited with error") {
			t.Errorf("Notes = %q", res.Notes)
		}
	})
}

// With no jscodeshift on PATH, which is what the shipped image does, the call
// is a reported no-op and never an error.
func TestRewriteWithJscodeshift_MissingBinaryIsReportedNotApplied(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	res, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), "k", "true", true)
	if err != nil {
		t.Fatalf("a missing binary must not be an error: %v", err)
	}
	if res.TransformApplied || !strings.Contains(res.Notes, "not found on PATH") {
		t.Fatalf("got applied=%v notes=%q", res.TransformApplied, res.Notes)
	}
}

func TestRewriteWithJscodeshift_CancelledContextStopsTheRun(t *testing.T) {
	installShim(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := RewriteWithJscodeshift(ctx, t.TempDir(), "k", "true", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.TransformApplied {
		t.Fatalf("a cancelled context still produced an applied transform")
	}
}

// ---- Go side: process environment and command line -----------------------------

// TestRewriteWithJscodeshift_ChildRunsInCallersWorkingDirectory pins that the
// child inherits the caller's working directory and is not moved into the temp
// directory that holds the transform.
func TestRewriteWithJscodeshift_ChildRunsInCallersWorkingDirectory(t *testing.T) {
	installShim(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cwdPath := filepath.Join(t.TempDir(), "cwd")
	t.Setenv("SHIM_CWD_TO", cwdPath)
	if _, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), "k", "true", true); err != nil {
		t.Fatalf("RewriteWithJscodeshift: %v", err)
	}
	raw, err := os.ReadFile(cwdPath)
	if err != nil {
		t.Fatalf("the shim did not record its working directory: %v", err)
	}
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(raw)))
	want, _ := filepath.EvalSymlinks(wd)
	if got != want {
		t.Errorf("tool ran in %s, want the caller's working directory %s", got, want)
	}
}

// TestRewriteWithJscodeshift_RunEnvironmentAndCleanup checks the temp artifacts
// that now carry request data: a private directory holding exactly the two
// files, and nothing left behind.
func TestRewriteWithJscodeshift_RunEnvironmentAndCleanup(t *testing.T) {
	scratch, repo, out := t.TempDir(), t.TempDir(), t.TempDir()
	installExecutable(t, "#!/bin/sh\n"+
		`ls -ld "$(dirname "$2")" > "`+out+`/mode"`+"\n"+
		`ls "$(dirname "$2")" > "`+out+`/files"`+"\n")
	t.Setenv("TMPDIR", scratch) // after every t.TempDir() above

	if _, err := RewriteWithJscodeshift(context.Background(), repo, "k", "true", false); err != nil {
		t.Fatalf("RewriteWithJscodeshift: %v", err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("the stand-in did not record %s: %v", name, err)
		}
		return strings.TrimSpace(string(b))
	}
	if mode := read("mode"); !strings.HasPrefix(mode, "drwx------") {
		t.Errorf("transform dir is not private (0700): %s", mode)
	}
	if files := read("files"); files != "params.json\ntransform.cjs" {
		t.Errorf("transform dir holds %q", files)
	}
	left, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatalf("reading scratch: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("temp dir leaked: %d entries left in TMPDIR", len(left))
	}
}

// TestRewriteWithJscodeshift_ArgvIsExact pins the whole command line: no extra
// flag may ever be added, --dry appears exactly when dryRun is set (dropping it
// would make a preview rewrite files in place), and the repository path stays
// the last argument.
func TestRewriteWithJscodeshift_ArgvIsExact(t *testing.T) {
	installShim(t)
	for _, dryRun := range []bool{true, false} {
		argvPath := filepath.Join(t.TempDir(), "argv.json")
		t.Setenv("SHIM_ARGV_TO", argvPath)
		repo := t.TempDir()
		if _, err := RewriteWithJscodeshift(context.Background(), repo, "k", "true", dryRun); err != nil {
			t.Fatalf("dryRun=%v: %v", dryRun, err)
		}
		raw, err := os.ReadFile(argvPath)
		if err != nil {
			t.Fatalf("dryRun=%v: %v", dryRun, err)
		}
		var args []string
		if err := json.Unmarshal(raw, &args); err != nil {
			t.Fatalf("dryRun=%v: %v", dryRun, err)
		}
		for i, a := range args {
			if strings.HasSuffix(a, string(os.PathSeparator)+transformFileName) {
				args[i] = "<transform>"
			}
		}
		want := []string{"--transform", "<transform>", "--extensions", "ts,tsx,js,jsx", "--ignore-pattern", "node_modules", repo}
		if dryRun {
			want = append([]string{"--dry"}, want...)
		}
		if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("dryRun=%v: argv = %q, want %q", dryRun, args, want)
		}
	}
}

// ---- the data channel and failure modes -------------------------------------------

// TestWriteTransform_ParamsContentAndModes pins the data channel: the exact JSON
// the transform parses, private file modes, and that only exact variant
// spellings select the if branch.
func TestWriteTransform_ParamsContentAndModes(t *testing.T) {
	for _, v := range []string{" true", "true ", "\ttrue"} {
		if got := newTransformParams("k", v).BranchIndex; got != 1 {
			t.Errorf("variant %q: branchIndex = %d, want 1 (only exact spellings enable)", v, got)
		}
	}
	dir := t.TempDir()
	transformPath, err := writeTransform(dir, "k", "true")
	if err != nil {
		t.Fatalf("writeTransform: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, paramsFileName))
	if err != nil {
		t.Fatalf("reading params: %v", err)
	}
	if string(raw) != `{"flagKey":"k","branchIndex":0}` {
		t.Errorf("params.json = %s", raw)
	}
	for _, p := range []string{filepath.Join(dir, paramsFileName), transformPath} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", filepath.Base(p), st.Mode().Perm())
		}
	}
}

// A failed run must clean up too: params.json holds request data.
func TestRewriteWithJscodeshift_FailedRunStillCleansUp(t *testing.T) {
	scratch := t.TempDir()
	installExecutable(t, "#!/bin/sh\necho ' MOD /r/a.ts'\nexit 1\n")
	t.Setenv("TMPDIR", scratch)
	if _, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), "k", "true", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries, err := os.ReadDir(scratch) // t.TempDir() above ran after TMPDIR was set, so match our prefix only
	if err != nil {
		t.Fatalf("reading scratch: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tombstone-jscodeshift-") {
			t.Errorf("temp dir %s leaked after a failed run", e.Name())
		}
	}
}

// If params.json cannot be parsed when the transform loads, the run must fail
// loudly: a silent no-op would report success while rewriting nothing.
func TestRewriteWithJscodeshift_UnreadableParamsFailLoudly(t *testing.T) {
	src := shimSource
	hook := "const transformPath = args[at + 1];"
	if !strings.Contains(src, hook) {
		t.Fatalf("shim no longer contains %q", hook)
	}
	corrupt := "\nfs.writeFileSync(require('path').join(require('path').dirname(transformPath), 'params.json'), 'not json');"
	installExecutable(t, strings.Replace(src, hook, hook+corrupt, 1))
	res, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), "k", "true", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.TransformApplied {
		t.Fatalf("a transform with unreadable params still reported success")
	}
}

// TestTransform_UsesTheJscodeshiftAPIAsIntended pins what the transform asks
// jscodeshift for, in order. The shim's stub ignores the argument of find(), so
// this variant of the shim records every call instead.
func TestTransform_UsesTheJscodeshiftAPIAsIntended(t *testing.T) {
	src := shimSource
	for _, rep := range [][2]string{
		{"const replaced = [];", "const replaced = [];\nconst calls = [];"},
		{"if (typeof arg === 'string') {", "if (typeof arg === 'string') { calls.push(['root', arg]);"},
		{"find: function () {", "find: function (t) { calls.push(['find', t === undefined ? null : t]);"},
		{"toSource: function () { return 'ok'; },", "toSource: function (o) { calls.push(['toSource', o]); return 'ok'; },"},
		{"return { replaceWith: function (nodes) { replaced.push(nodes); } };",
			"calls.push(['wrap', arg && arg.node !== undefined ? 'path' : 'node']);\n  return { replaceWith: function (nodes) { replaced.push(nodes); } };"},
		{"if (process.env.SHIM_RESULT_TO) {",
			"if (process.env.SHIM_CALLS_TO) { fs.writeFileSync(process.env.SHIM_CALLS_TO, JSON.stringify(calls)); }\nif (process.env.SHIM_RESULT_TO) {"},
	} {
		if !strings.Contains(src, rep[0]) {
			t.Fatalf("shim no longer contains %q", rep[0])
		}
		src = strings.Replace(src, rep[0], rep[1], 1)
	}
	installExecutable(t, src)
	scenario, err := json.Marshal([]map[string]any{ifNode(callWith(member("evaluate"), stringLiteral("k")), block("I"), block("E"))})
	if err != nil {
		t.Fatalf("encoding scenario: %v", err)
	}
	callsPath := filepath.Join(t.TempDir(), "calls.json")
	t.Setenv("SHIM_SCENARIO", string(scenario))
	t.Setenv("SHIM_CALLS_TO", callsPath)
	if res, err := RewriteWithJscodeshift(context.Background(), t.TempDir(), "k", "true", true); err != nil || !res.TransformApplied {
		t.Fatalf("run failed: %v %+v", err, res)
	}
	raw, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("reading calls: %v", err)
	}
	var got, want any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("calls are not JSON: %v", err)
	}
	wantRaw := `[["root","src"],["find","IfStatement"],["wrap","path"],["toSource",{"quote":"single"}]]`
	if err := json.Unmarshal([]byte(wantRaw), &want); err != nil {
		t.Fatalf("bad expectation: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("API calls = %s, want %s", raw, wantRaw)
	}
}
