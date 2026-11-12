package verify

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/KIFFCollective/agentsign/internal/signers"
)

func readSigners(path string) (*signers.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("allowed_signers: %w", err)
	}
	defer f.Close()
	return signers.Parse(f)
}

// WriteText writes a plain table.
func (r *Report) WriteText(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "COMMIT\tSTATUS\tCOMMITTER\tAGENT KEY\tDETAIL")
	for _, res := range r.Results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", short(res.Commit), res.Status, res.Committer, keyCol(res), detail(res))
	}
	return tw.Flush()
}

// WriteMarkdown writes a table suitable for $GITHUB_STEP_SUMMARY.
func (r *Report) WriteMarkdown(w io.Writer) error {
	icon := map[Status]string{StatusOK: "✅", StatusFail: "❌", StatusHuman: "👤"}
	title := "All agent commits verified"
	if !r.OK() {
		title = "Agent commit verification failed"
	}
	fmt.Fprintf(w, "### agentsign: %s\n\n", title)
	fmt.Fprintln(w, "| Commit | Status | Committer | Agent key | Detail |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, res := range r.Results {
		fmt.Fprintf(w, "| `%s` | %s %s | %s | %s | %s |\n",
			short(res.Commit), icon[res.Status], res.Status, md(res.Committer), md(keyCol(res)), md(detail(res)))
	}
	return nil
}

// WriteJSON writes the report as JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func keyCol(r Result) string {
	if r.Fingerprint == "" {
		return "-"
	}
	return r.Fingerprint
}

func detail(r Result) string {
	if r.Reason != "" {
		return r.Reason
	}
	return r.Subject
}

func md(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
