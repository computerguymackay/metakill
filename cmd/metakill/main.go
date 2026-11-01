// Command metakill strips metadata from images, documents, audio and video.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/computerguymackay/metakill/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

const groupKey = "metakill_group"

var groupOrder = []string{"Input", "Output", "Safety", "Stripping", "Other"}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	var cfg cli.Config
	var selftest bool
	code := cli.ExitOK

	cmd := &cobra.Command{
		Use:   "metakill [flags] FILE...\n  metakill -d [flags] DIR...",
		Short: "Strip identifying metadata from images, documents, audio and video",
		Long: `metakill removes identifying metadata (GPS, timestamps, device and software
strings, authors, comments, thumbnails and, by default, media ID tags) from
images, documents, audio and video files. Files are edited in place unless
-o/--output is given; run with -n/--dry-run first to see what would go.`,
		Version:       version,
		Args:          cobra.ArbitraryArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(c *cobra.Command, a []string) error {
			if selftest {
				code = cli.SelfTest(c.OutOrStdout(), c.ErrOrStderr())
				return nil
			}
			cfg.Args = a
			cfg.Stdout = c.OutOrStdout()
			cfg.Stderr = c.ErrOrStderr()
			cfg.Stdin = os.Stdin
			cfg.IsTTY = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
			code = cli.Run(cfg)
			return nil
		},
	}
	cmd.SetVersionTemplate("metakill {{.Version}}\n")

	f := cmd.Flags()
	f.SortFlags = false
	add := func(group, name string) { _ = f.SetAnnotation(name, groupKey, []string{group}) }

	f.BoolVarP(&cfg.Directory, "directory", "d", false, "treat arguments as directories, not files")
	add("Input", "directory")
	f.BoolVarP(&cfg.Recursive, "recursive", "r", false, "recurse into subdirectories (only with -d)")
	add("Input", "recursive")

	f.BoolVarP(&cfg.DryRun, "dry-run", "n", false, "report what would be removed; modify nothing")
	add("Output", "dry-run")
	f.BoolVarP(&cfg.JSON, "json", "j", false, "print one JSON document instead of plain text")
	add("Output", "json")
	f.BoolVarP(&cfg.Quiet, "quiet", "q", false, "print errors only")
	add("Output", "quiet")
	f.BoolVarP(&cfg.Verbose, "verbose", "v", false, "show one line per field with its namespace")
	add("Output", "verbose")
	f.StringVarP(&cfg.Output, "output", "o", "", "write the cleaned file to `PATH` (single-file mode only)")
	add("Output", "output")

	f.BoolVarP(&cfg.Yes, "yes", "y", false, "do not ask for confirmation (required without a TTY)")
	add("Safety", "yes")
	f.BoolVarP(&cfg.Backup, "backup", "b", true, "create FILE.bak before editing in place; --backup=false disables")
	add("Safety", "backup")

	f.BoolVarP(&cfg.KeepTags, "keep-tags", "k", false, "keep Title/Artist/Album/Track/Year media ID tags")
	add("Stripping", "keep-tags")
	f.BoolVarP(&cfg.KeepThumbnails, "keep-thumbnails", "t", false, "keep embedded thumbnails and cover art (implies -k)")
	add("Stripping", "keep-thumbnails")
	f.BoolVarP(&cfg.KeepICC, "keep-icc", "c", false, "keep ICC colour profiles in images")
	add("Stripping", "keep-icc")
	f.BoolVarP(&cfg.NoZeroPad, "no-zero-pad", "z", false, "do not zero existing padding/free-space regions")
	add("Stripping", "no-zero-pad")

	f.BoolVar(&selftest, "selftest", false, "run the built-in smoke test")
	_ = f.MarkHidden("selftest")

	cobra.AddTemplateFunc("groupedFlags", groupedFlags)
	cmd.SetUsageTemplate(usageTemplate)
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return flagError{err}
	})

	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		var fe flagError
		if errors.As(err, &fe) {
			fmt.Fprintf(os.Stderr, "metakill: error: %v\n%s\n", fe.err, cli.UsageHint)
			return cli.ExitUsage
		}
		fmt.Fprintf(os.Stderr, "metakill: error: %v\n%s\n", err, cli.UsageHint)
		return cli.ExitUsage
	}
	return code
}

type flagError struct{ err error }

func (e flagError) Error() string { return e.err.Error() }

const usageTemplate = `Usage:
  {{.UseLine}}
{{groupedFlags .}}
Exit codes:
  0  success, every file fully clean
  1  one or more files failed (or the run was aborted)
  2  usage error; nothing was modified
  3  no failures, but some metadata could not be removed
`

// groupedFlags renders the flag help in fixed groups.
func groupedFlags(c *cobra.Command) string {
	groups := map[string]*pflag.FlagSet{}
	for _, g := range groupOrder {
		fs := pflag.NewFlagSet(g, pflag.ContinueOnError)
		fs.SortFlags = false
		groups[g] = fs
	}
	c.LocalFlags().VisitAll(func(fl *pflag.Flag) {
		if fl.Hidden {
			return
		}
		g := "Other"
		if v, ok := fl.Annotations[groupKey]; ok && len(v) > 0 {
			g = v[0]
		}
		groups[g].AddFlag(fl)
	})
	var b strings.Builder
	for _, g := range groupOrder {
		fs := groups[g]
		if !fs.HasFlags() {
			continue
		}
		fmt.Fprintf(&b, "\n%s:\n%s", g, fs.FlagUsages())
	}
	return b.String()
}
