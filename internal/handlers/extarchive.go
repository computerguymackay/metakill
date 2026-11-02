package handlers

// Shared machinery for archive formats that metakill can read in pure Go but
// can only *write* with an external tool (7z, RAR). Reading, dry runs and
// post-write validation never run anything external; only repacking does,
// and only with -x/--external-tools.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/computerguymackay/metakill/report"
)

// cleanEpoch is the timestamp given to every file before repacking.
var cleanEpoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// maxExtMember caps how much of one member is cleaned in memory. It is a
// variable so tests can lower it.
var maxExtMember int64 = 512 << 20

// extEntry is one archive member as seen by the shared pipeline.
type extEntry struct {
	name     string
	isDir    bool
	isLink   bool
	exec     bool
	size     int64
	modified time.Time
	created  time.Time
	accessed time.Time
	owner    string // "user:group (uid:gid)" if the format records one
	open     func() (io.ReadCloser, error)
}

// extRun accumulates the report for one pass over an archive. When dir is
// non-empty, cleaned members are written below it.
type extRun struct {
	opts       Options
	rep        report.Report
	dir        string
	latest     time.Time
	otherTimes int
	owners     map[string]bool
	warnedPath bool
	files      int
}

func newExtRun(format string, opts Options, dir string) *extRun {
	return &extRun{opts: opts, rep: report.New(format), dir: dir, owners: map[string]bool{}}
}

// safeMemberPath rejects names that would escape the extraction directory.
func safeMemberPath(name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	if n == "" || strings.HasPrefix(n, "/") || (len(n) > 1 && n[1] == ':') {
		return "", fmt.Errorf("unsafe member path %q", name)
	}
	clean := path.Clean(n)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe member path %q", name)
	}
	return clean, nil
}

// add processes one member.
func (x *extRun) add(e extEntry) error {
	if e.modified.After(cleanEpoch.Add(48*time.Hour)) && e.modified.After(x.latest) {
		x.latest = e.modified
	}
	if !e.created.IsZero() || !e.accessed.IsZero() {
		x.otherTimes++
	}
	if e.owner != "" {
		x.owners[e.owner] = true
	}
	if e.isLink {
		return fmt.Errorf("member %q is a symbolic or hard link, which is %w", e.name, ErrUnsupported)
	}
	rel, err := safeMemberPath(e.name)
	if err != nil {
		return err
	}
	if cat, ok := junkKind(rel); ok {
		if !e.isDir {
			x.rep.Add(report.Binary(cat, "archive", rel, e.size))
		}
		return nil
	}
	if !x.warnedPath && homePath.MatchString(rel) {
		x.rep.Warn(fmt.Sprintf("member names look like absolute or home-directory paths (e.g. %q); names are not changed", rel))
		x.warnedPath = true
	}
	target := ""
	if x.dir != "" {
		target = filepath.Join(x.dir, filepath.FromSlash(rel))
	}
	if e.isDir {
		if target != "" {
			return os.MkdirAll(target, 0o755)
		}
		return nil
	}
	x.files++
	rc, err := e.open()
	if err != nil {
		return fmt.Errorf("%s: %w", e.name, err)
	}
	defer func() { _ = rc.Close() }()

	dst := io.Discard // io.Writer
	if target != "" {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if e.exec {
			mode = 0o755
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		dst = f
	}
	prefix, err := io.ReadAll(io.LimitReader(rc, sniffSize))
	if err != nil {
		return fmt.Errorf("%s: %w", e.name, err)
	}
	h := Detect(bytes.NewReader(prefix), "")
	if h == nil || e.size > maxExtMember {
		if h != nil {
			x.rep.Unclean(fmt.Sprintf("%s (%s, %d bytes) is too large to clean in memory; copied unchanged", rel, h.Name(), e.size))
		}
		if _, err := dst.Write(prefix); err != nil {
			return err
		}
		_, err := io.Copy(dst, rc)
		return err
	}
	rest, err := io.ReadAll(io.LimitReader(rc, maxExtMember+1))
	if err != nil {
		return fmt.Errorf("%s: %w", e.name, err)
	}
	data := append(prefix, rest...)
	out, sub, ok := StripEmbedded(rel, data, x.opts)
	if ok {
		if sub.TotalFields() > 0 || !sub.FullyClean || len(sub.Warnings) > 0 {
			x.rep.AddEmbedded(sub)
		}
		if out != nil {
			data = out
		}
	}
	_, err = dst.Write(data)
	return err
}

// finish adds the archive-level fields gathered while walking members.
func (x *extRun) finish() {
	if !x.latest.IsZero() {
		x.rep.Add(report.Text(report.CatTimestamp, "archive", "member timestamps (latest)", x.latest.UTC().Format(time.RFC3339)))
	}
	if x.otherTimes > 0 {
		x.rep.Add(report.Text(report.CatTimestamp, "archive", "creation/access times", fmt.Sprintf("%d member(s)", x.otherTimes)))
	}
	if len(x.owners) > 0 {
		x.rep.Add(report.Text(report.CatAuthor, "archive", "owners", strings.Join(sortedKeys2(x.owners), ", ")))
	}
}

// normaliseTree sets every timestamp to cleanEpoch and permissions to
// 755/644 so the repacked archive inherits clean values.
func normaliseTree(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return os.Chmod(p, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := os.Chmod(p, mode); err != nil {
			return err
		}
		return os.Chtimes(p, cleanEpoch, cleanEpoch)
	})
	if err != nil {
		return err
	}
	// Directories last, deepest first, so file changes do not bump them.
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		if err := os.Chtimes(d, cleanEpoch, cleanEpoch); err != nil {
			return err
		}
	}
	return nil
}

// ---------- external tools ----------

// extTool is a resolved external archiver.
type extTool struct {
	kind string // "7z", "bsdtar" or "rar"
	path string // absolute path
}

func (t extTool) String() string { return t.kind + " at " + t.path }

func lookTool(names []string, extra []string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
		}
	}
	for _, p := range extra {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func windowsProgramFiles(rel string) []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	var out []string
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		if dir := os.Getenv(env); dir != "" {
			out = append(out, filepath.Join(dir, rel))
		}
	}
	return out
}

// toolLookup can be replaced by tests.
var toolLookup = func(kind string) (extTool, bool) {
	switch kind {
	case "7z":
		if p := lookTool([]string{"7zz", "7z", "7za", "7zr"}, windowsProgramFiles(`7-Zip\7z.exe`)); p != "" {
			return extTool{"7z", p}, true
		}
		if p := lookTool([]string{"bsdtar", "tar"}, nil); p != "" && isBsdtar(p) {
			return extTool{"bsdtar", p}, true
		}
	case "rar":
		if p := lookTool([]string{"rar"}, windowsProgramFiles(`WinRAR\Rar.exe`)); p != "" {
			return extTool{"rar", p}, true
		}
	}
	return extTool{}, false
}

// isBsdtar reports whether a tar binary is libarchive's bsdtar (the macOS
// and Windows tar), which can write 7z archives.
func isBsdtar(p string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, "--version").Output()
	return err == nil && bytes.Contains(out, []byte("libarchive"))
}

// pack runs the tool in dir to create out from dir's contents.
func (t extTool) pack(dir, out string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("archive has no members to repack: %w", ErrUnsupported)
	}
	var args []string
	switch t.kind {
	case "7z":
		// -mtc/-mta off: store no creation or access times. Modification
		// times are stored but are all cleanEpoch.
		args = []string{"a", "-t7z", "-mx=5", "-mtc=off", "-mta=off", "-bd", "-y", "--", out}
	case "bsdtar":
		args = []string{"--format", "7zip", "-cf", out, "--"}
	case "rar":
		// -ma5: RAR5; -idq: quiet; no -ow (owners), -z (comment) or -ts
		// options, so only modification times (cleanEpoch) are stored.
		args = []string{"a", "-ma5", "-r", "-idq", "-y", "--", out}
	default:
		return fmt.Errorf("unknown tool %q", t.kind)
	}
	for _, e := range entries {
		args = append(args, e.Name())
	}
	cmd := exec.Command(t.path, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return fmt.Errorf("%s failed: %v: %s", t, err, msg)
	}
	return nil
}

// repack extracts with walk into a private temp directory, normalises it,
// runs the tool and copies the new archive into w.
func repack(t extTool, format string, opts Options, w io.Writer, walk func(x *extRun) error) (report.Report, error) {
	root, err := os.MkdirTemp("", "metakill-")
	if err != nil {
		return report.Report{}, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	content := filepath.Join(root, "c")
	if err := os.Mkdir(content, 0o700); err != nil {
		return report.Report{}, err
	}
	x := newExtRun(format, opts, content)
	if err := walk(x); err != nil {
		return x.rep, err
	}
	x.finish()
	if err := normaliseTree(content); err != nil {
		return x.rep, err
	}
	out := filepath.Join(root, "out."+format)
	if format == "rar" && t.kind != "rar" {
		out = filepath.Join(root, "out.7z")
	}
	if err := t.pack(content, out); err != nil {
		return x.rep, err
	}
	f, err := os.Open(out)
	if err != nil {
		return x.rep, err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(w, f)
	return x.rep, err
}

// toolNote explains why an archive is only inspected.
func toolNote(format string, opts Options, found bool) string {
	switch {
	case !opts.ExternalTools:
		return fmt.Sprintf("%s archives can only be cleaned with an external tool; re-run with -x/--external-tools to repack it", format)
	case !found && format == "RAR":
		return "no rar/WinRAR found to repack RAR; install it, or re-run with -a/--convert-rar to convert to 7z (needs 7-Zip)"
	case !found:
		return "no 7-Zip (7zz, 7z, 7za) or bsdtar found to repack 7z"
	}
	return ""
}

// readerAt returns r as an io.ReaderAt, reading it into memory if needed.
func readerAt(r io.ReadSeeker) (io.ReaderAt, int64, error) {
	size, err := streamSize(r)
	if err != nil {
		return nil, 0, err
	}
	if ra, ok := r.(io.ReaderAt); ok {
		return ra, size, nil
	}
	b, err := readAll(r)
	if err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(b), size, nil
}
