// Package cli orchestrates a metakill run: it collects files, picks handlers,
// asks for confirmation, processes each file and emits the report.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/computerguymackay/metakill/internal/handlers"
	"github.com/computerguymackay/metakill/internal/report"
)

// Exit codes.
const (
	ExitOK       = 0 // everything processed and fully clean
	ExitError    = 1 // at least one file failed
	ExitUsage    = 2 // bad flags or arguments; nothing was touched
	ExitNotClean = 3 // no failures, but some metadata could not be removed
)

// Config is the fully parsed command line.
type Config struct {
	Directory      bool
	Recursive      bool
	DryRun         bool
	JSON           bool
	Quiet          bool
	Output         string
	Yes            bool
	Backup         bool
	KeepTags       bool
	KeepThumbnails bool
	KeepICC        bool
	NoZeroPad      bool
	Verbose        bool
	Args           []string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// IsTTY reports whether stdin is interactive. Nil means "not a TTY".
	IsTTY func() bool
}

// UsageHint is appended to every usage error.
const UsageHint = "Run 'metakill --help' for usage."

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// Run executes one invocation and returns the process exit code.
func Run(cfg Config) int {
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.Stdin == nil {
		cfg.Stdin = os.Stdin
	}
	if cfg.KeepThumbnails {
		cfg.KeepTags = true
	}
	r := &runner{cfg: cfg, opts: handlers.Options{
		KeepTags:       cfg.KeepTags,
		KeepThumbnails: cfg.KeepThumbnails,
		KeepICC:        cfg.KeepICC,
		ZeroPad:        !cfg.NoZeroPad,
		DryRun:         cfg.DryRun,
	}}
	code, err := r.run()
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(cfg.Stderr, "metakill: error: %s\n%s\n", ue.msg, UsageHint)
			return ExitUsage
		}
		fmt.Fprintf(cfg.Stderr, "metakill: error: %v\n", err)
		return ExitError
	}
	return code
}

type runner struct {
	cfg  Config
	opts handlers.Options
	run_ report.Run
}

// job is one file to process. If rep is non-nil the file was already
// resolved (skipped or failed) during collection.
type job struct {
	path    string
	handler handlers.Handler
	rep     *report.Report
}

func (r *runner) run() (int, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	modifying := !r.cfg.DryRun
	needConfirm := modifying && !r.cfg.Yes && r.cfg.Output == ""
	if needConfirm && (r.cfg.IsTTY == nil || !r.cfg.IsTTY()) {
		return 0, usagef("refusing to modify files in a non-interactive session; pass -y/--yes to confirm")
	}

	jobs, err := r.collect()
	if err != nil {
		return 0, err
	}

	if needConfirm {
		ok, err := r.confirm(jobs)
		if err != nil {
			return 0, err
		}
		if !ok {
			fmt.Fprintln(r.cfg.Stderr, "metakill: aborted; no files were modified")
			return ExitError, nil
		}
	}

	r.run_.DryRun = r.cfg.DryRun
	for _, j := range jobs {
		var rep report.Report
		if j.rep != nil {
			rep = *j.rep
		} else {
			rep = r.processOne(j)
		}
		rep.DryRun = r.cfg.DryRun
		rep.Normalize()
		r.emitOne(&rep)
		r.run_.Results = append(r.run_.Results, rep)
	}
	r.run_.Finish()

	if r.cfg.JSON {
		if err := report.WriteJSON(r.cfg.Stdout, &r.run_); err != nil {
			return 0, err
		}
	} else if !r.cfg.Quiet && (r.cfg.Directory || len(r.run_.Results) > 1) {
		report.WriteTextSummary(r.cfg.Stdout, &r.run_)
	}

	s := r.run_.Summary
	switch {
	case s.Errors > 0:
		return ExitError, nil
	case s.NotFullyClean > 0:
		return ExitNotClean, nil
	default:
		return ExitOK, nil
	}
}

func (r *runner) validate() error {
	c := r.cfg
	if len(c.Args) == 0 {
		if c.Directory {
			return usagef("no directories given")
		}
		return usagef("no files given")
	}
	if c.Directory && c.Output != "" {
		return usagef("-o/--output can only be used with a single file, not with -d/--directory")
	}
	if !c.Directory && c.Output != "" && len(c.Args) != 1 {
		return usagef("-o/--output needs exactly one input file, got %d", len(c.Args))
	}
	if c.Recursive && !c.Directory && !c.Quiet {
		fmt.Fprintln(c.Stderr, "metakill: warning: -r/--recursive has no effect without -d/--directory")
	}
	for _, a := range c.Args {
		st, err := os.Stat(a)
		if err != nil {
			continue // reported per file later
		}
		if c.Directory && !st.IsDir() {
			return usagef("%s is a file; in directory mode (-d) every argument must be a directory", a)
		}
		if !c.Directory && st.IsDir() {
			return usagef("%s is a directory; use -d/--directory to process directories", a)
		}
	}
	return nil
}

func (r *runner) collect() ([]job, error) {
	var paths []string
	var jobs []job
	if r.cfg.Directory {
		for _, d := range r.cfg.Args {
			found, err := walk(filepath.Clean(d), r.cfg.Recursive)
			if err != nil {
				rep := errorReport(filepath.Clean(d), err)
				jobs = append(jobs, job{path: d, rep: &rep})
				continue
			}
			paths = append(paths, found...)
		}
	} else {
		for _, a := range r.cfg.Args {
			paths = append(paths, filepath.Clean(a))
		}
	}
	for _, p := range paths {
		jobs = append(jobs, r.classify(p))
	}
	return jobs, nil
}

// classify opens a file and picks a handler, or produces a skip/error report.
func (r *runner) classify(path string) job {
	f, err := os.Open(path)
	if err != nil {
		rep := errorReport(path, err)
		return job{path: path, rep: &rep}
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		rep := errorReport(path, err)
		return job{path: path, rep: &rep}
	}
	if !st.Mode().IsRegular() {
		rep := errorReport(path, errors.New("not a regular file"))
		return job{path: path, rep: &rep}
	}
	h := handlers.Detect(f, path)
	if h == nil {
		rep := report.New("unknown")
		rep.Path = path
		rep.Status = report.StatusSkipped
		rep.Warn("unrecognized file type; skipped")
		return job{path: path, rep: &rep}
	}
	return job{path: path, handler: h}
}

func errorReport(path string, err error) report.Report {
	rep := report.New("unknown")
	rep.Path = path
	rep.Status = report.StatusError
	rep.FullyClean = false
	rep.Error = err.Error()
	return rep
}

// emitOne prints per-file text output and stderr diagnostics.
func (r *runner) emitOne(rep *report.Report) {
	c := r.cfg
	switch rep.Status {
	case report.StatusError:
		fmt.Fprintf(c.Stderr, "metakill: error: %s: %s\n", rep.Path, rep.Error)
		return
	case report.StatusSkipped:
		if !c.Quiet && !c.JSON {
			for _, w := range rep.Warnings {
				fmt.Fprintf(c.Stderr, "metakill: warning: %s: %s\n", rep.Path, w)
			}
		}
		return
	}
	if !c.JSON && !c.Quiet {
		report.WriteText(c.Stdout, rep, report.TextOptions{Verbose: c.Verbose})
	}
	if !rep.FullyClean {
		verb := "not fully scrubbed"
		if c.DryRun {
			verb = "cannot be fully scrubbed"
		}
		fmt.Fprintf(c.Stderr, "metakill: error: %s: %s: %s\n", rep.Path, verb, strings.Join(uncleanReasons(rep), "; "))
	} else if !c.Quiet && !c.JSON {
		for _, w := range allWarnings(rep) {
			fmt.Fprintf(c.Stderr, "metakill: warning: %s: %s\n", rep.Path, w)
		}
	}
}

func allWarnings(rep *report.Report) []string {
	out := append([]string(nil), rep.Warnings...)
	for i := range rep.Embedded {
		for _, w := range allWarnings(&rep.Embedded[i]) {
			out = append(out, rep.Embedded[i].Path+": "+w)
		}
	}
	return out
}

func uncleanReasons(rep *report.Report) []string {
	ws := allWarnings(rep)
	if len(ws) == 0 {
		return []string{"handler could not remove all metadata"}
	}
	return ws
}

// confirm runs an inspect pass, prints a summary and asks once.
func (r *runner) confirm(jobs []job) (bool, error) {
	n, fields := 0, 0
	for _, j := range jobs {
		if j.rep != nil {
			continue
		}
		rep, err := r.inspect(j)
		if err != nil {
			continue
		}
		if c := rep.TotalFields(); c > 0 {
			n++
			fields += c
		}
	}
	if n == 0 {
		return true, nil
	}
	backup := "on"
	if !r.cfg.Backup {
		backup = "off"
	}
	fmt.Fprintf(r.cfg.Stderr, "metakill: remove %d metadata fields from %d file(s) in place (backups %s)? [y/N] ", fields, n, backup)
	line, err := bufio.NewReader(r.cfg.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}

func (r *runner) inspect(j job) (report.Report, error) {
	f, err := os.Open(j.path)
	if err != nil {
		return report.Report{}, err
	}
	defer func() { _ = f.Close() }()
	return j.handler.Inspect(f, r.opts)
}
