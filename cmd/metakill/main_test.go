package main

// CLI integration tests: they build the real binary and run it with os/exec
// against copies of testdata/fixtures, asserting exact stdout, stderr and
// exit codes. Golden files live in testdata/golden; regenerate them with
//
//	go test ./cmd/metakill -update

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/computerguymackay/metakill/internal/fixtures"
)

var update = flag.Bool("update", false, "rewrite golden files")

var binPath string

func TestMain(m *testing.M) {
	flag.Parse()
	dir, err := os.MkdirTemp("", "metakill-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := "metakill"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binPath = filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type result struct {
	stdout, stderr string
	code           int
}

// run executes the binary in dir with stdin closed (non-interactive).
func runCLI(t *testing.T, dir string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Stdin = nil
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{out.String(), errb.String(), code}
}

func fixturesDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func fixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(fixturesDir(t))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// copyFixtures copies the named fixtures (all if none) into a temp dir.
func copyFixtures(t *testing.T, names ...string) string {
	t.Helper()
	if len(names) == 0 {
		names = fixtureNames(t)
	}
	dir := t.TempDir()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(fixturesDir(t), n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (run with -update): %v", path, err)
	}
	w := strings.ReplaceAll(string(want), "\r\n", "\n")
	if compressorDependent(name) {
		got, w = maskBytes(got), maskBytes(w)
	}
	if got != w {
		t.Errorf("%s mismatch.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// compressorDependent reports fixtures whose byte counts depend on the Go
// version's DEFLATE output (ZIP-based formats recompress changed members).
func compressorDependent(golden string) bool {
	for _, ext := range []string{".docx.", ".odt.", ".epub.", ".zip.", ".jar."} {
		if strings.Contains(golden, ext) {
			return true
		}
	}
	return false
}

var byteCounts = regexp.MustCompile(`("bytes_(removable|saved)": )\d+|~[0-9.]+ (B|KiB|MiB) removable`)

// maskBytes hides byte counts so golden comparisons of ZIP-based formats do
// not depend on the Go version. Every field and value is still compared.
func maskBytes(s string) string {
	return byteCounts.ReplaceAllString(s, "${1}N")
}

func expect(t *testing.T, r result, code int, stdout, stderr string) {
	t.Helper()
	if r.code != code {
		t.Errorf("exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
	if stdout != "*" && r.stdout != stdout {
		t.Errorf("stdout:\n%q\nwant:\n%q", r.stdout, stdout)
	}
	if stderr != "*" && r.stderr != stderr {
		t.Errorf("stderr:\n%q\nwant:\n%q", r.stderr, stderr)
	}
}

// ---------- golden dry-run output ----------

func TestGoldenDryRun(t *testing.T) {
	dir := copyFixtures(t)
	for _, n := range fixtureNames(t) {
		t.Run(n, func(t *testing.T) {
			txt := runCLI(t, dir, "-n", n)
			if txt.code != 0 {
				t.Errorf("exit %d: %s", txt.code, txt.stderr)
			}
			golden(t, filepath.Join("text", n+".txt"), txt.stdout)
			js := runCLI(t, dir, "-n", "-j", n)
			if js.code != 0 {
				t.Errorf("exit %d: %s", js.code, js.stderr)
			}
			if !json.Valid([]byte(js.stdout)) {
				t.Fatalf("invalid JSON:\n%s", js.stdout)
			}
			golden(t, filepath.Join("json", n+".json"), js.stdout)
			vb := runCLI(t, dir, "-n", "-v", n)
			golden(t, filepath.Join("verbose", n+".txt"), vb.stdout)
		})
	}
}

// ---------- usage errors ----------

func TestHelpListsEveryFlag(t *testing.T) {
	r := runCLI(t, t.TempDir(), "-h")
	expect(t, r, 0, "*", "")
	for _, f := range []string{
		"-d, --directory", "-r, --recursive", "-n, --dry-run", "-j, --json", "-q, --quiet",
		"-o, --output", "-y, --yes", "-b, --backup", "-k, --keep-tags", "-t, --keep-thumbnails",
		"-c, --keep-icc", "-z, --no-zero-pad", "-v, --verbose", "-h, --help", "--version",
	} {
		if !strings.Contains(r.stdout, f) {
			t.Errorf("help is missing %q", f)
		}
	}
	if strings.Contains(r.stdout, "selftest") {
		t.Error("hidden --selftest flag shown in help")
	}
}

func TestUsageErrors(t *testing.T) {
	dir := copyFixtures(t, "photo.jpg")
	hint := "Run 'metakill --help' for usage.\n"
	cases := []struct {
		name   string
		args   []string
		stderr string
	}{
		{"unknown long flag", []string{"--bogus", "photo.jpg"}, "metakill: error: unknown flag: --bogus\n" + hint},
		{"unknown short flag", []string{"-x", "photo.jpg"}, "metakill: error: unknown shorthand flag: 'x' in -x\n" + hint},
		{"no args", nil, "metakill: error: no files given\n" + hint},
		{"file in dir mode", []string{"-d", "photo.jpg"}, "metakill: error: photo.jpg is a file; in directory mode (-d) every argument must be a directory\n" + hint},
		{"dir in file mode", []string{"."}, "metakill: error: . is a directory; use -d/--directory to process directories\n" + hint},
		{"output with dir", []string{"-d", "-o", "x", "."}, "metakill: error: -o/--output can only be used with a single file, not with -d/--directory\n" + hint},
		{"output with two files", []string{"-o", "x", "photo.jpg", "photo.jpg"}, "metakill: error: -o/--output needs exactly one input file, got 2\n" + hint},
		{"no tty without -y", []string{"photo.jpg"}, "metakill: error: refusing to modify files in a non-interactive session; pass -y/--yes to confirm\n" + hint},
	}
	before, _ := os.ReadFile(filepath.Join(dir, "photo.jpg"))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expect(t, runCLI(t, dir, c.args...), 2, "", c.stderr)
		})
	}
	after, _ := os.ReadFile(filepath.Join(dir, "photo.jpg"))
	if !bytes.Equal(before, after) {
		t.Fatal("a usage error modified the file")
	}
}

func TestVersion(t *testing.T) {
	expect(t, runCLI(t, t.TempDir(), "--version"), 0, "metakill dev\n", "")
}

func TestSelftest(t *testing.T) {
	r := runCLI(t, t.TempDir(), "--selftest")
	if r.code != 0 || !strings.Contains(r.stdout, "fixtures passed") {
		t.Fatalf("selftest failed: %+v", r)
	}
}

// ---------- directory mode ----------

func setupTree(t *testing.T) string {
	dir := copyFixtures(t, "photo.jpg", "notes.md")
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(fixturesDir(t), "song.mp3"))
	for name, data := range map[string][]byte{
		filepath.Join(sub, "song.mp3"):    b,
		filepath.Join(dir, "data.xyz"):    []byte("unknown format"),
		filepath.Join(dir, "old.jpg.bak"): b, // backups are never processed
	} {
		if err := os.WriteFile(name, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDirectoryMode(t *testing.T) {
	dir := setupTree(t)
	sep := string(filepath.Separator)

	r := runCLI(t, dir, "-n", "-d", ".")
	expect(t, r, 0, "*", "metakill: warning: data.xyz: unrecognized file type; skipped\n")
	for _, want := range []string{"notes.md  [TEXT]", "photo.jpg  [JPEG]", "3 files: 1 cleaned, 1 already clean, 1 skipped, 0 errors (dry run)"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "song.mp3") || strings.Contains(r.stdout, "old.jpg") {
		t.Errorf("non-recursive run descended or processed a backup:\n%s", r.stdout)
	}
	// Order is lexicographic.
	if strings.Index(r.stdout, "notes.md") > strings.Index(r.stdout, "photo.jpg") {
		t.Error("output not in lexicographic order")
	}

	r = runCLI(t, dir, "-n", "-d", "-r", ".")
	if !strings.Contains(r.stdout, "sub"+sep+"song.mp3  [MP3]") {
		t.Errorf("recursive run missed sub/song.mp3:\n%s", r.stdout)
	}

	r = runCLI(t, dir, "-n", "-q", "-d", "-r", ".")
	expect(t, r, 0, "", "")

	r = runCLI(t, dir, "-n", "-j", "-d", "-r", ".")
	var doc struct {
		Results []struct {
			Path, Status string
		}
		Summary map[string]any
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range doc.Results {
		got = append(got, x.Path+"="+x.Status)
	}
	want := []string{"data.xyz=skipped", "notes.md=already_clean", "photo.jpg=cleaned", "sub" + sep + "song.mp3=cleaned"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("results %v, want %v", got, want)
	}
	if r.stderr != "" {
		t.Errorf("JSON mode printed warnings to stderr: %q", r.stderr)
	}

	// Multiple directories.
	r = runCLI(t, dir, "-n", "-d", ".", "sub")
	if !strings.Contains(r.stdout, "sub"+sep+"song.mp3") || !strings.Contains(r.stdout, "photo.jpg") {
		t.Errorf("multi-directory run incomplete:\n%s", r.stdout)
	}

	r = runCLI(t, dir, "-n", "-r", "photo.jpg")
	expect(t, r, 0, "*", "metakill: warning: -r/--recursive has no effect without -d/--directory\n")
}

// ---------- real runs ----------

func TestRealRunWithBackup(t *testing.T) {
	dir := copyFixtures(t, "photo.jpg")
	orig, _ := os.ReadFile(filepath.Join(dir, "photo.jpg"))

	r := runCLI(t, dir, "-y", "photo.jpg")
	expect(t, r, 0, "*", "")
	if !strings.Contains(r.stdout, "Backup:          photo.jpg.bak") || !strings.Contains(r.stdout, "fields removed") {
		t.Errorf("unexpected stdout:\n%s", r.stdout)
	}
	bak, err := os.ReadFile(filepath.Join(dir, "photo.jpg.bak"))
	if err != nil || !bytes.Equal(bak, orig) {
		t.Fatal("backup missing or different from the original")
	}
	cleaned, _ := os.ReadFile(filepath.Join(dir, "photo.jpg"))
	if bytes.Contains(cleaned, []byte("iPhone")) {
		t.Fatal("file not cleaned")
	}

	// A second run finds nothing and does not touch the existing backup.
	expect(t, runCLI(t, dir, "-y", "photo.jpg"), 0, "photo.jpg  [JPEG]\n  no removable metadata found\n  Kept:            Orientation = \"6\"\n", "")

	// An existing backup blocks a new in-place edit.
	if err := os.WriteFile(filepath.Join(dir, "photo.jpg"), orig, 0o644); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t, dir, "-y", "photo.jpg")
	expect(t, r, 1, "", "metakill: error: photo.jpg: backup photo.jpg.bak already exists; remove it or pass --backup=false\n")
	cur, _ := os.ReadFile(filepath.Join(dir, "photo.jpg"))
	if !bytes.Equal(cur, orig) {
		t.Fatal("file modified despite backup conflict")
	}

	expect(t, runCLI(t, dir, "-y", "-q", "--backup=false", "photo.jpg"), 0, "", "")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".metakill-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestOutputFlag(t *testing.T) {
	dir := copyFixtures(t, "song.mp3")
	orig, _ := os.ReadFile(filepath.Join(dir, "song.mp3"))
	r := runCLI(t, dir, "-o", "clean.mp3", "song.mp3") // -o never prompts
	expect(t, r, 0, "*", "")
	if !strings.Contains(r.stdout, "Output:          clean.mp3") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
	cur, _ := os.ReadFile(filepath.Join(dir, "song.mp3"))
	if !bytes.Equal(cur, orig) {
		t.Fatal("-o modified the input")
	}
	if _, err := os.Stat(filepath.Join(dir, "song.mp3.bak")); err == nil {
		t.Fatal("-o created a backup")
	}
	out, _ := os.ReadFile(filepath.Join(dir, "clean.mp3"))
	if bytes.Contains(out, []byte("ID3")) {
		t.Fatal("output still tagged")
	}
}

func TestKeepTagsCombination(t *testing.T) {
	dir := copyFixtures(t, "song.mp3")
	expect(t, runCLI(t, dir, "-y", "-q", "-k", "song.mp3"), 0, "", "")
	r := runCLI(t, dir, "-n", "-j", "-k", "song.mp3")
	var doc struct {
		Results []struct {
			Fields []map[string]any
			Kept   []struct{ Key string }
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Results[0].Fields) != 0 {
		t.Errorf("fields left after -k strip: %v", doc.Results[0].Fields)
	}
	var keys []string
	for _, k := range doc.Results[0].Kept {
		keys = append(keys, k.Key)
	}
	if strings.Join(keys, ",") != "TIT2,TPE1,TALB,TRCK,TDRC" {
		t.Errorf("kept %v", keys)
	}
	// Without -k the kept tags are now removable.
	r = runCLI(t, dir, "-n", "song.mp3")
	if !strings.Contains(r.stdout, "ID tags:") {
		t.Errorf("expected ID tags to be removable without -k:\n%s", r.stdout)
	}
}

// The JSON schema must be identical between dry and real runs apart from the
// dry_run value and the byte-count label.
func TestJSONSchemaMatchesBetweenModes(t *testing.T) {
	dir := copyFixtures(t, "photo.jpg")
	dry := runCLI(t, dir, "-n", "-j", "photo.jpg")
	real := runCLI(t, dir, "-y", "-j", "--backup=false", "photo.jpg")
	shape := func(s string) []string {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		var keys []string
		var walk func(p string, v any)
		walk = func(p string, v any) {
			switch x := v.(type) {
			case map[string]any:
				for k, c := range x {
					k = strings.Replace(k, "bytes_removable", "bytes_*", 1)
					k = strings.Replace(k, "bytes_saved", "bytes_*", 1)
					walk(p+"."+k, c)
				}
			case []any:
				if len(x) > 0 {
					walk(p+"[]", x[0])
				}
			}
			keys = append(keys, p)
		}
		walk("", v)
		sort.Strings(keys)
		return keys
	}
	if a, b := shape(dry.stdout), shape(real.stdout); !reflect.DeepEqual(a, b) {
		t.Errorf("schemas differ:\ndry:  %v\nreal: %v", a, b)
	}
	if !strings.Contains(dry.stdout, `"bytes_removable"`) || !strings.Contains(real.stdout, `"bytes_saved"`) {
		t.Error("byte-count labels wrong")
	}
}

func TestNotFullyCleanExitCode(t *testing.T) {
	dir := t.TempDir()
	// A HEIF with a thumbnail item cannot be fully cleaned without -t.
	b := heicWithThumb(t)
	if err := os.WriteFile(filepath.Join(dir, "thumb.heic"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, dir, "-n", "-q", "thumb.heic")
	expect(t, r, 3, "", "metakill: error: thumb.heic: cannot be fully scrubbed: 1 embedded thumbnail image(s) kept: removing them would require rewriting item locations\n")
	r = runCLI(t, dir, "-n", "-j", "-q", "thumb.heic")
	if r.code != 3 || !strings.Contains(r.stdout, `"fully_clean": false`) {
		t.Errorf("JSON run: code %d\n%s", r.code, r.stdout)
	}
	expect(t, runCLI(t, dir, "-n", "-q", "-t", "thumb.heic"), 0, "", "")
}

func TestMissingFile(t *testing.T) {
	r := runCLI(t, t.TempDir(), "-n", "nope.jpg")
	if r.code != 1 || !strings.HasPrefix(r.stderr, "metakill: error: nope.jpg: ") {
		t.Errorf("got %+v", r)
	}
}

func TestCorruptFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.png"), []byte("\x89PNG\r\n\x1a\ngarbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, dir, "-y", "bad.png")
	if r.code != 1 || !strings.Contains(r.stderr, "bad.png: png:") {
		t.Errorf("got %+v", r)
	}
}

func heicWithThumb(t *testing.T) []byte {
	t.Helper()
	return fixtures.PhoneHEIC(true)
}

// TestCorpus runs metakill over a directory of real-world samples, if
// METAKILL_CORPUS points at one (it is never checked in). It copies the
// corpus, strips it, and checks nothing errors and nothing removable is left.
//
//	METAKILL_CORPUS=~/metakill-samples go test ./cmd/metakill -run Corpus -v
func TestCorpus(t *testing.T) {
	src := os.Getenv("METAKILL_CORPUS")
	if src == "" {
		t.Skip("METAKILL_CORPUS not set")
	}
	dir := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	type doc struct {
		Results []struct {
			Path, Status, Error string
			FullyClean          bool `json:"fully_clean"`
			Warnings            []string
		}
		Summary map[string]any
	}
	parse := func(r result) doc {
		var d doc
		if err := json.Unmarshal([]byte(r.stdout), &d); err != nil {
			t.Fatalf("bad JSON: %v\n%s", err, r.stdout)
		}
		return d
	}
	strip := parse(runCLI(t, dir, "-y", "-j", "--backup=false", "-d", "-r", "."))
	for _, r := range strip.Results {
		switch {
		case r.Status == "error":
			t.Errorf("%s: %s", r.Path, r.Error)
		case !r.FullyClean && r.Status != "skipped":
			t.Logf("not fully clean: %s: %v", r.Path, r.Warnings)
		}
	}
	after := parse(runCLI(t, dir, "-n", "-j", "-d", "-r", "."))
	if n, _ := after.Summary["fields_removed"].(float64); n != 0 {
		t.Errorf("%v fields still removable after stripping", n)
	}
	t.Logf("strip summary: %v", strip.Summary)
}

func TestZipArchiveRealRun(t *testing.T) {
	dir := copyFixtures(t, "bundle.zip", "plugin.jar")
	jar, _ := os.ReadFile(filepath.Join(dir, "plugin.jar"))

	r := runCLI(t, dir, "-y", "-q", "--backup=false", "bundle.zip", "plugin.jar")
	expect(t, r, 0, "", "")

	// Re-inspecting finds nothing left, and the signed JAR is untouched.
	r = runCLI(t, dir, "-n", "bundle.zip", "plugin.jar")
	expect(t, r, 0,
		"bundle.zip  [ZIP]\n  no removable metadata found\n"+
			"2 files: 0 cleaned, 1 already clean, 1 skipped, 0 errors (dry run); 0 fields removable, ~0 B removable\n",
		"metakill: warning: bundle.zip: member names look like absolute or home-directory paths (e.g. \"Users/jane/notes.txt\"); names are not changed\n"+
			"metakill: warning: plugin.jar: signed Java or browser-extension package (JAR/XPI): repacking would break its signature or integrity checks; skipped\n")
	after, _ := os.ReadFile(filepath.Join(dir, "plugin.jar"))
	if !bytes.Equal(after, jar) {
		t.Fatal("signed JAR was modified")
	}
}

func TestZipInDirectoryMode(t *testing.T) {
	dir := copyFixtures(t, "bundle.zip", "plugin.jar", "photo.jpg")
	r := runCLI(t, dir, "-n", "-j", "-d", ".")
	var doc struct {
		Results []struct{ Path, Format, Status string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range doc.Results {
		got = append(got, x.Path+"="+x.Format+"/"+x.Status)
	}
	want := []string{"bundle.zip=zip/cleaned", "photo.jpg=jpeg/cleaned", "plugin.jar=zip/skipped"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if r.code != 0 {
		t.Errorf("exit %d", r.code)
	}
}
