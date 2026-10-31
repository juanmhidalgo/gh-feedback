// gh-feedback fetches, triages and resolves the review feedback of a pull
// request. Run as `gh feedback <command>`.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/cli/go-gh/v2"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/spf13/cobra"

	"github.com/juanmhidalgo/gh-feedback/internal/feedback"
)

// exitError ends the process with code after its output was already printed.
type exitError struct{ code int }

func (e exitError) Error() string { return "exit " + strconv.Itoa(e.code) }

func main() {
	if err := rootCmd().Execute(); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var repoFlag string

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "feedback",
		Short:         "Fetch, triage and resolve the review feedback of a pull request",
		Version:       version(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Annotations:   map[string]string{cobra.CommandDisplayNameAnnotation: "gh feedback"},
	}
	root.CompletionOptions.HiddenDefaultCmd = true
	root.PersistentFlags().StringVarP(&repoFlag, "repo", "R", "", "repository as [HOST/]OWNER/REPO (default: the current one)")
	root.AddCommand(fetchCmd(), resolveCmd(), dismissCmd(), reactCmd(), watchCmd())
	return root
}

// version is the module version Go stamps from the git tag, or "dev".
func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func currentRepo() (repository.Repository, error) {
	if repoFlag != "" {
		return repository.Parse(repoFlag)
	}
	return repository.Current()
}

func newClient() (*feedback.Client, repository.Repository, error) {
	repo, err := currentRepo()
	if err != nil {
		return nil, repo, err
	}
	c, err := feedback.New(repo)
	return c, repo, err
}

func prArg(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid PR number: %q", s)
	}
	return n, nil
}

func fetchCmd() *cobra.Command {
	var opts feedback.FetchOptions
	var split string
	cmd := &cobra.Command{
		Use:   "fetch <pr>",
		Short: "Print every comment, inline comment and review of a PR as JSON",
		Long: `Print every comment, inline comment and review of a PR as JSON, every page
of each. Inline comments carry their thread's state (resolved, outdated,
thread_id, thread_comments); resolved threads are left out unless
--include-resolved.

With --split DIR each comment is written to DIR/<ref_id>.json (":" becomes
"_") and stdout carries the metadata without the bodies, with comment_file
pointing at each file.

Exits 3 when the review threads cannot be read.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, err := prArg(args[0])
			if err != nil {
				return err
			}
			opts.PR = pr
			// Split files feed whoever evaluates the comments: keep bodies whole there.
			if split != "" && !cmd.Flags().Changed("max-body") {
				opts.MaxBody = 0
			}
			c, _, err := newClient()
			if err != nil {
				return err
			}
			res, err := c.Fetch(opts)
			if err != nil {
				var te *feedback.ThreadsError
				if errors.As(err, &te) {
					fmt.Fprintln(os.Stderr, `{"status":"error","error":"Could not read review threads (GraphQL)"}`)
					return exitError{3}
				}
				return err
			}
			var out any = res
			if split != "" {
				if out, err = feedback.Split(res, split); err != nil {
					return err
				}
			}
			b, err := feedback.Encode(out)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(b)
			return err
		},
	}
	cmd.Flags().IntVar(&opts.MaxBody, "max-body", 1500, "truncate bodies to N characters, 0 keeps them whole (default 0 with --split)")
	cmd.Flags().BoolVar(&opts.BotsOnly, "bots-only", false, "only comments from bots")
	cmd.Flags().BoolVar(&opts.IncludeResolved, "include-resolved", false, "keep comments of resolved threads")
	cmd.Flags().StringVar(&split, "split", "", "write one file per comment to `DIR` and print metadata only")
	return cmd
}

// emit prints an action's status line and turns its code into the exit code.
func emit(r feedback.Report) error {
	if r.Stderr {
		fmt.Fprintln(os.Stderr, r.JSON())
	} else {
		fmt.Println(r.JSON())
	}
	if r.Code != 0 {
		return exitError{r.Code}
	}
	return nil
}

func seenFlag(cmd *cobra.Command, seen int) *int {
	if cmd.Flags().Changed("seen") {
		return &seen
	}
	return nil
}

const actionExitCodes = `
Prints one JSON status line. Exit codes: 0 done · 1 error, nothing posted ·
5 reply posted but the thread was not resolved · 6 thread changed since
triage, nothing posted.`

const seenHelp = "the thread's comment count when triage read it (thread_comments); a different count now skips the thread with exit 6"

func resolveCmd() *cobra.Command {
	var seen int
	cmd := &cobra.Command{
		Use:   "resolve <pr> <ref_id>",
		Short: "Resolve the review thread of an inline comment",
		Long:  "Resolve the review thread of an inline comment (review_comment:N).\n" + actionExitCodes,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return act(args, func(c *feedback.Client, pr int, ref feedback.Ref) feedback.Report {
				return c.Resolve(pr, ref, seenFlag(cmd, seen))
			})
		},
	}
	cmd.Flags().IntVar(&seen, "seen", 0, seenHelp)
	return cmd
}

func dismissCmd() *cobra.Command {
	var seen int
	var reason string
	cmd := &cobra.Command{
		Use:   "dismiss <pr> <ref_id>",
		Short: "Reply that a comment was dismissed, and resolve its thread",
		Long: `Reply that a comment was reviewed and dismissed, with --reason, and resolve
its thread. An inline comment's thread is read again before posting: one
resolved meanwhile is skipped (exit 6). Issue comments and reviews have no
thread, so they get a new comment that quotes their id.
` + actionExitCodes,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return act(args, func(c *feedback.Client, pr int, ref feedback.Ref) feedback.Report {
				return c.Dismiss(pr, ref, seenFlag(cmd, seen), reason)
			})
		},
	}
	cmd.Flags().IntVar(&seen, "seen", 0, seenHelp)
	cmd.Flags().StringVar(&reason, "reason", "", "why the comment was dismissed")
	return cmd
}

func reactCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "react <ref_id>",
		Short: "Add an eyes reaction to acknowledge a comment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := feedback.ParseRef(args[0])
			if err != nil {
				return emit(feedback.ErrorReport(err.Error()))
			}
			c, _, err := newClient()
			if err != nil {
				return err
			}
			return emit(c.React(ref))
		},
	}
}

func act(args []string, do func(*feedback.Client, int, feedback.Ref) feedback.Report) error {
	pr, err := prArg(args[0])
	if err != nil {
		return err
	}
	ref, err := feedback.ParseRef(args[1])
	if err != nil {
		return emit(feedback.ErrorReport(err.Error()))
	}
	c, _, err := newClient()
	if err != nil {
		return err
	}
	return emit(do(c, pr, ref))
}

func watchCmd() *cobra.Command {
	var copilot bool
	var timeout, interval, grace int
	cmd := &cobra.Command{
		Use:   "watch <pr>",
		Short: "Wait for a PR's CI checks and, optionally, Copilot's review",
		Long: `Wait for a PR's CI checks and, with --copilot, a Copilot review of the PR's
current head commit. Prints one line per event, then a final block:

  CI: pass | fail | none | pending
  FAILED: <check name> <link>      (one per failed or cancelled check)
  COPILOT: <N> inline comments | not-waited | pending
  COPILOT_REVIEW: <review url>     (when Copilot reviewed)

Exit codes: 0 finished · 1 usage or API error · 3 timed out (the final block
is still printed).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, err := prArg(args[0])
			if err != nil {
				return err
			}
			c, repo, err := newClient()
			if err != nil {
				return err
			}
			name := repo.Owner + "/" + repo.Name
			ghRepo := name
			if repo.Host != "" && repo.Host != "github.com" {
				ghRepo = repo.Host + "/" + name
			}
			w := &feedback.Watcher{
				Client: c, Repo: name, PR: pr, Copilot: copilot,
				Timeout:       time.Duration(timeout) * time.Second,
				Interval:      time.Duration(interval) * time.Second,
				NoChecksGrace: time.Duration(grace) * time.Second,
				Checks: func() []feedback.Check {
					stdout, _, _ := gh.Exec("pr", "checks", strconv.Itoa(pr), "-R", ghRepo, "--json", "name,bucket,link")
					return feedback.ParseChecks(stdout.Bytes())
				},
				Now:   time.Now,
				Sleep: time.Sleep,
				Out:   os.Stdout,
			}
			timedOut, err := w.Run()
			if err != nil {
				return err
			}
			if timedOut {
				return exitError{3}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&copilot, "copilot", false, "also wait for a Copilot review of the current head commit")
	cmd.Flags().IntVar(&timeout, "timeout", 1800, "give up after N seconds")
	cmd.Flags().IntVar(&interval, "interval", 30, "poll every N seconds")
	cmd.Flags().IntVar(&grace, "no-checks-grace", 180, "seconds without any check before CI counts as none")
	return cmd
}
