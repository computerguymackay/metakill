package report

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// TextOptions controls the plain-text emitter.
type TextOptions struct {
	Verbose bool
}

// WriteText writes one file's report in the human-readable format.
func WriteText(w io.Writer, r *Report, o TextOptions) {
	writeText(w, r, o, "")
}

func writeText(w io.Writer, r *Report, o TextOptions, indent string) {
	r.Normalize()
	fmt.Fprintf(w, "%s%s  [%s]\n", indent, r.Path, strings.ToUpper(r.Format))
	in := indent + "  "
	if r.Output != "" {
		fmt.Fprintf(w, "%s%-17s%s\n", in, "Output:", r.Output)
	}
	if r.Backup != "" {
		fmt.Fprintf(w, "%s%-17s%s\n", in, "Backup:", r.Backup)
	}
	if len(r.Fields) == 0 && len(r.Embedded) == 0 {
		fmt.Fprintf(w, "%sno removable metadata found\n", in)
	}
	writeGroups(w, r.Fields, o, in, "")
	if len(r.Kept) > 0 {
		if o.Verbose {
			writeGroups(w, r.Kept, o, in, "Kept ")
		} else {
			fmt.Fprintf(w, "%s%-17s%s\n", in, "Kept:", joinFields(r.Kept))
		}
	}
	for i := range r.Embedded {
		e := r.Embedded[i]
		e.DryRun = r.DryRun
		writeText(w, &e, o, in+"> ")
	}
	if indent == "" && (len(r.Fields) > 0 || len(r.Embedded) > 0) {
		n := r.TotalFields()
		if r.DryRun {
			fmt.Fprintf(w, "%s--- %s, ~%s removable\n", in, plural(n, "field"), HumanBytes(r.Bytes))
		} else {
			fmt.Fprintf(w, "%s--- %s removed, %s saved\n", in, plural(n, "field"), HumanBytes(r.Bytes))
		}
	}
}

func writeGroups(w io.Writer, fields []Field, o TextOptions, in, prefix string) {
	for _, cat := range CategoryOrder {
		var group []Field
		for _, f := range fields {
			if f.Category == cat {
				group = append(group, f)
			}
		}
		if len(group) == 0 {
			continue
		}
		label := prefix + cat.Label() + ":"
		if o.Verbose {
			fmt.Fprintf(w, "%s%s\n", in, label)
			for _, f := range group {
				fmt.Fprintf(w, "%s    %s\n", in, formatField(f, true))
			}
			continue
		}
		fmt.Fprintf(w, "%s%-17s%s\n", in, label, joinFields(group))
	}
}

func joinFields(fs []Field) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = formatField(f, false)
	}
	return strings.Join(parts, ", ")
}

func formatField(f Field, withNS bool) string {
	key := f.Key
	if withNS && f.Namespace != "" {
		key = f.Namespace + ":" + f.Key
	}
	var val string
	if f.Value != nil {
		val = strconv.Quote(*f.Value)
	} else {
		val = fmt.Sprintf("<binary, %d bytes>", f.Size)
	}
	if f.Key == "<embedded>" && !withNS {
		return val
	}
	return key + " = " + val
}

// WriteTextSummary writes the closing summary line for a multi-file run.
func WriteTextSummary(w io.Writer, run *Run) {
	s := run.Summary
	verb := "removed"
	bytesLabel := HumanBytes(s.Bytes) + " saved"
	if run.DryRun {
		verb = "removable"
		bytesLabel = "~" + HumanBytes(s.Bytes) + " removable"
	}
	fmt.Fprintf(w, "%s: %d cleaned, %d already clean, %d skipped, %d errors",
		plural(s.Files, "file"), s.Cleaned, s.AlreadyClean, s.Skipped, s.Errors)
	if s.Inspected > 0 {
		fmt.Fprintf(w, ", %d inspected only", s.Inspected)
	}
	if run.DryRun {
		// In dry-run nothing is cleaned; "cleaned" counts files that would be.
		fmt.Fprint(w, " (dry run)")
	}
	fmt.Fprintf(w, "; %d fields %s, %s\n", s.FieldsRemoved, verb, bytesLabel)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// HumanBytes formats a byte count with binary units.
func HumanBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%.1f KiB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f MiB", float64(n)/(k*k))
	default:
		return fmt.Sprintf("%.1f GiB", float64(n)/(k*k*k))
	}
}
