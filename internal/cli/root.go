package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/getspas/spas/internal/app"
	"github.com/getspas/spas/internal/appdirs"
	"github.com/getspas/spas/internal/gitexec"
	"github.com/getspas/spas/internal/githubref"
	"github.com/getspas/spas/internal/interaction"
	"github.com/getspas/spas/internal/linkstate"
	"github.com/getspas/spas/internal/provider"
	"github.com/getspas/spas/internal/spaserr"
	"github.com/getspas/spas/internal/version"
	"github.com/spf13/cobra"
)

type rootOptions struct {
	repo           string
	nonInteractive bool
	yes            bool
	json           bool
	gitPath        string
	verbose        bool
}

func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := NewRootContext(ctx, os.Stdin, os.Stdout, os.Stderr)
	if err := root.Execute(); err != nil {
		if ctx.Err() != nil {
			err = spaserr.Wrap(spaserr.KindInterrupted, fmt.Errorf("interrupted: %w", err))
		}
		err = classifyExecutionError(err)
		options, _ := root.Context().Value(rootOptionsKey{}).(*rootOptions)
		jsonMode := options != nil && options.json
		if !jsonMode {
			jsonMode = jsonRequested(os.Args[1:])
		}
		reported := false
		var outputWritten interface{ OutputWritten() bool }
		if errors.As(err, &outputWritten) {
			reported = outputWritten.OutputWritten()
		}
		if jsonMode {
			if !reported {
				payload := map[string]any{
					"code":    errorCode(err),
					"message": err.Error(),
				}
				_ = json.NewEncoder(root.ErrOrStderr()).Encode(map[string]any{
					"ok":    false,
					"error": payload,
				})
			}
		} else {
			_, _ = fmt.Fprintln(root.ErrOrStderr(), "error:", err)
		}
		return exitCode(err)
	}
	return 0
}

func jsonRequested(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--" {
			return false
		}
		if argument == "--json" || argument == "--json=true" {
			return true
		}
	}
	return false
}

func NewRootContext(parent context.Context, in io.Reader, out, errOut io.Writer) *cobra.Command {
	options := &rootOptions{}
	ctx := context.WithValue(parent, rootOptionsKey{}, options)
	root := &cobra.Command{
		Use:   "spas",
		Short: "Manage project assets through a linked GitHub repository",
		Long: `SPAS synchronizes selected project assets with a linked GitHub repository
while keeping them at their normal paths in the project workspace.

SPAS writes exact paths only to the project repository's local exclude file.
It never edits .gitignore or global Git exclusions, and it never creates a
commit in the project repository.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version.Version,
		PersistentPreRunE: func(command *cobra.Command, _ []string) error {
			if !options.verbose || options.json {
				return nil
			}
			repository := options.repo
			if repository == "" {
				repository = "."
			}
			gitPath := options.gitPath
			if gitPath == "" {
				gitPath = "git"
			}
			_, err := fmt.Fprintf(
				command.ErrOrStderr(),
				"spas: command=%s repo=%q git=%q\n",
				command.Name(),
				repository,
				gitPath,
			)
			return err
		},
	}
	root.SetContext(ctx)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return spaserr.Wrap(spaserr.KindInvalidUsage, err)
	})

	root.PersistentFlags().StringVar(&options.repo, "repo", "", "project Git workspace to operate on (default: current directory)")
	root.PersistentFlags().BoolVar(&options.nonInteractive, "non-interactive", false, "never prompt; fail when a required choice is not supplied")
	root.PersistentFlags().BoolVarP(&options.yes, "yes", "y", false, "accept recommended non-destructive setup confirmations")
	root.PersistentFlags().BoolVar(&options.json, "json", false, "write machine-readable JSON and disable prompts")
	root.PersistentFlags().StringVar(&options.gitPath, "git", "", "Git executable to use instead of searching PATH")
	root.PersistentFlags().BoolVarP(&options.verbose, "verbose", "v", false, "show additional diagnostics without file contents")

	root.AddCommand(
		newLinkCommand(options),
		newAddCommand(options),
		newRemoveCommand(options),
		newSyncCommand(options),
		newStatusCommand(options),
		newDiffCommand(options),
		newDoctorCommand(options),
		newUnlinkCommand(options),
		newCompletionCommand(),
		newVersionCommand(),
	)
	for _, command := range root.Commands() {
		if command.Args == nil {
			continue
		}
		inner := command.Args
		command.Args = func(cmd *cobra.Command, args []string) error {
			if err := inner(cmd, args); err != nil {
				return spaserr.Wrap(spaserr.KindInvalidUsage, err)
			}
			return nil
		}
	}
	return root
}

func newLinkCommand(root *rootOptions) *cobra.Command {
	var transport string
	var branch string
	var replace bool
	var dryRun bool
	command := &cobra.Command{
		Use:   "link [OWNER/REPOSITORY | GITHUB-URL]",
		Short: "Link this project workspace to a GitHub repository",
		Long: `Create a local association between this project workspace and a linked GitHub
repository. link performs no network request, clone, fetch, file copy,
local-exclude update, or Git configuration change.`,
		Example: `  # Interactive
  spas link

  # One line
  spas link getspas/project-assets --transport ssh --branch main --non-interactive`,
		Args: cobra.MaximumNArgs(1),
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if transport == "" {
				return nil
			}
			return validateEnum("--transport", transport, "https", "ssh")
		},
		RunE: func(command *cobra.Command, args []string) error {
			instance, err := buildApp(command, root)
			if err != nil {
				return err
			}
			repository := ""
			if len(args) == 1 {
				repository = args[0]
			} else {
				repository, err = instance.Prompt.Input(command.Context(), "GitHub repository (OWNER/REPOSITORY or URL):")
				if err != nil {
					return err
				}
			}
			selectedTransport := provider.Transport(transport)
			if selectedTransport == "" && !strings.Contains(repository, "://") && !strings.Contains(repository, "@") {
				if instance.Prompt.Interactive {
					value, err := instance.Prompt.Select(command.Context(), "Git transport:", []interaction.Option{
						{Key: "1", Value: "https", Label: "HTTPS"},
						{Key: "2", Value: "ssh", Label: "SSH"},
					})
					if err != nil {
						return err
					}
					selectedTransport = provider.Transport(value)
				} else {
					selectedTransport = provider.HTTPS
				}
			}
			return instance.Link(command.Context(), app.LinkOptions{
				Repository: repository,
				Transport:  selectedTransport,
				Branch:     branch,
				Replace:    replace,
				DryRun:     dryRun,
			})
		},
	}
	command.Flags().StringVar(&transport, "transport", "", "Git transport for OWNER/REPOSITORY: https or ssh")
	command.Flags().StringVar(&branch, "branch", "", "branch in the linked repository; otherwise discover it during first sync")
	command.Flags().BoolVar(&replace, "replace", false, "replace an existing local link without deleting its managed checkout")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate and show the link without saving it")
	return command
}

func newAddCommand(root *rootOptions) *cobra.Command {
	var existing string
	var merge string
	var skipTracked bool
	var dryRun bool
	command := &cobra.Command{
		Use:   "add PATH...",
		Short: "Add files to SPAS management",
		Long: `Enroll exact regular files as managed assets. A directory argument expands
to its current regular files; it does not claim future files in that directory.

add updates only the project repository's local exclude file and local SPAS
state. It does not contact GitHub or create a commit.`,
		Example: `  # Interactive
  spas add config/dev.json testdata/mock-api.json

  # One line
  spas add config/dev.json testdata/mock-api.json --existing-exclude preserve --merge-protection enable --non-interactive`,
		Args: cobra.MinimumNArgs(1),
		PreRunE: func(command *cobra.Command, args []string) error {
			if err := validateEnum("--existing-exclude", existing, "ask", "preserve", "abort"); err != nil {
				return err
			}
			return validateEnum("--merge-protection", merge, "ask", "enable", "skip", "require")
		},
		RunE: func(command *cobra.Command, args []string) error {
			instance, err := buildApp(command, root)
			if err != nil {
				return err
			}
			return instance.Add(command.Context(), app.AddOptions{
				Paths:           args,
				SkipTracked:     skipTracked,
				ExistingExclude: app.ExistingExcludePolicy(existing),
				MergeProtection: app.MergeProtectionPolicy(merge),
				DryRun:          dryRun,
			})
		},
	}
	command.Flags().StringVar(&existing, "existing-exclude", "ask", "existing local-exclude policy: ask, preserve, or abort")
	command.Flags().StringVar(&merge, "merge-protection", "ask", "merge protection policy: ask, enable, skip, or require")
	command.Flags().BoolVar(&skipTracked, "skip-tracked", false, "skip paths already tracked by the project repository")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "show enrollment and local changes without persistent changes")
	return command
}

func newRemoveCommand(root *rootOptions) *cobra.Command {
	var missingOK bool
	var dryRun bool
	command := &cobra.Command{
		Use:   "remove PATH...",
		Short: "Mark managed assets for removal",
		Long: `Mark exact managed paths for deletion during the next sync. Removing a pending
addition cancels its enrollment and removes its SPAS exclusion. Otherwise,
remove does not delete workspace files, create a commit, push, or immediately
expose a managed path to the project repository.`,
		Example: `  spas remove docs/OLD-ARCHITECTURE.md
  spas remove docs/OLD-ARCHITECTURE.md --non-interactive`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			instance, err := buildApp(command, root)
			if err != nil {
				return err
			}
			return instance.Remove(command.Context(), app.RemoveOptions{Paths: args, MissingOK: missingOK, DryRun: dryRun})
		},
	}
	command.Flags().BoolVar(&missingOK, "missing-ok", false, "do not fail when a requested path is not managed by SPAS")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "show planned removals without saving them")
	return command
}

func newSyncCommand(root *rootOptions) *cobra.Command {
	var message string
	var messageFile string
	var conflict string
	var force bool
	var skip bool
	var discard bool
	var existing string
	var merge string
	var branch string
	var continueMerge bool
	var abortMerge bool
	var dryRun bool
	command := &cobra.Command{
		Use:   "sync",
		Short: "Synchronize managed assets with the linked repository",
		Long: `Synchronize approved changes between the project workspace and the linked repository.
sync clones or fetches when needed, creates at most one approved local-change
commit in the linked repository, merges remote changes, pushes without force,
and then writes the final assets. SPAS never creates a commit in the project repository.`,
		Example: `  # Interactive
  spas sync

  # One line
  spas sync --message "Update development assets" --conflict override \
    --existing-exclude preserve --merge-protection enable --non-interactive

  # Continue a merge in the linked repository after resolving files
  spas sync --continue --message "Resolve architecture merge"`,
		Args: cobra.NoArgs,
		PreRunE: func(command *cobra.Command, args []string) error {
			if command.Flags().Changed("message") && command.Flags().Changed("message-file") {
				return usageError("--message and --message-file are mutually exclusive")
			}
			modes := 0
			for _, enabled := range []bool{continueMerge, abortMerge, dryRun} {
				if enabled {
					modes++
				}
			}
			if modes > 1 {
				return usageError("--continue, --abort, and --dry-run are mutually exclusive")
			}
			if force && skip {
				return usageError("--force and --skip-conflicts are mutually exclusive")
			}
			if force && command.Flags().Changed("conflict") {
				return usageError("--force and --conflict are mutually exclusive")
			}
			if skip && command.Flags().Changed("conflict") {
				return usageError("--skip-conflicts and --conflict are mutually exclusive")
			}
			if skip && discard {
				return usageError("--discard-public-changes cannot be combined with --skip-conflicts")
			}
			if abortMerge {
				for _, flag := range []string{"message", "message-file", "conflict", "force", "skip-conflicts", "discard-public-changes", "existing-exclude", "merge-protection", "branch"} {
					if command.Flags().Changed(flag) {
						return usageError("--%s is not valid with --abort", flag)
					}
				}
			}
			if continueMerge {
				for _, flag := range []string{"conflict", "force", "skip-conflicts", "existing-exclude", "merge-protection", "branch"} {
					if command.Flags().Changed(flag) {
						return usageError("--%s is not valid with --continue", flag)
					}
				}
			} else if discard && !force && !(command.Flags().Changed("conflict") && conflict == string(app.ConflictOverride)) {
				return usageError("--discard-public-changes requires --force, --conflict=override, or --continue")
			}
			if err := validateEnum("--conflict", conflict, "ask", "skip", "override", "abort"); err != nil {
				return err
			}
			if err := validateEnum("--existing-exclude", existing, "ask", "preserve", "abort"); err != nil {
				return err
			}
			if err := validateEnum("--merge-protection", merge, "ask", "enable", "skip", "require"); err != nil {
				return err
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			instance, err := buildApp(command, root)
			if err != nil {
				return err
			}
			message, err = resolveCommitMessage(command, message, messageFile)
			if err != nil {
				return err
			}
			if force {
				conflict = string(app.ConflictOverride)
			}
			if skip {
				conflict = string(app.ConflictSkip)
			}
			if !command.Flags().Changed("conflict") && !force && !skip && !instance.Prompt.Interactive {
				// The documented non-interactive default is abort.
				conflict = string(app.ConflictAbort)
			}
			return instance.Sync(command.Context(), app.SyncOptions{
				Message:              message,
				Conflict:             app.ConflictPolicy(conflict),
				DiscardPublicChanges: discard,
				ExistingExclude:      app.ExistingExcludePolicy(existing),
				MergeProtection:      app.MergeProtectionPolicy(merge),
				Branch:               branch,
				Continue:             continueMerge,
				Abort:                abortMerge,
				DryRun:               dryRun,
			})
		},
	}
	command.Flags().StringVarP(&message, "message", "m", "", "approve one commit in the linked repository using this reason")
	command.Flags().StringVar(&messageFile, "message-file", "", "read the commit reason for the linked repository from a file")
	command.Flags().StringVar(&conflict, "conflict", "ask", "path conflict policy: ask, skip, override, or abort")
	command.Flags().BoolVar(&force, "force", false, "alias for --conflict=override")
	command.Flags().BoolVar(&skip, "skip-conflicts", false, "alias for --conflict=skip")
	command.Flags().BoolVar(&discard, "discard-public-changes", false, "allow ownership override or merge continuation to discard staged or unstaged changes in the project repository; requires --force, --conflict=override, or --continue")
	command.Flags().StringVar(&existing, "existing-exclude", "ask", "existing local-exclude policy: ask, preserve, or abort")
	command.Flags().StringVar(&merge, "merge-protection", "ask", "merge protection policy: ask, enable, skip, or require")
	command.Flags().StringVar(&branch, "branch", "", "branch in the linked repository for first sync; required for an empty remote")
	command.Flags().BoolVar(&continueMerge, "continue", false, "continue a merge in the linked repository after resolving files")
	command.Flags().BoolVar(&abortMerge, "abort", false, "abort a merge in the linked repository and restore its local pre-merge state")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "analyze local state without cloning, fetching, committing, pushing, or persistent changes")
	return command
}

func resolveCommitMessage(command *cobra.Command, message, messageFile string) (string, error) {
	if command.Flags().Changed("message-file") {
		if messageFile == "" {
			return "", usageError("--message-file requires a file path")
		}
		data, err := os.ReadFile(messageFile)
		if err != nil {
			return "", fmt.Errorf("read commit message file for linked repository: %w", err)
		}
		value := strings.TrimSpace(string(data))
		if value == "" {
			return "", usageError("commit message for linked repository must not be empty")
		}
		return value, nil
	}
	if command.Flags().Changed("message") && strings.TrimSpace(message) == "" {
		return "", usageError("commit message for linked repository must not be empty")
	}
	return message, nil
}

func newStatusCommand(root *rootOptions) *cobra.Command {
	var short bool
	var showPaths bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Show link, managed asset, and synchronization status",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			instance, err := buildApp(command, root)
			if err != nil {
				return err
			}
			return instance.Status(command.Context(), app.StatusOptions{Short: short, ShowPaths: showPaths})
		},
	}
	command.Flags().BoolVar(&short, "short", false, "show a compact text summary")
	command.Flags().BoolVar(&showPaths, "show-paths", false, "include project workspace and managed checkout paths")
	return command
}

func newDiffCommand(root *rootOptions) *cobra.Command {
	var nameOnly bool
	var stat bool
	var staged bool
	command := &cobra.Command{
		Use:   "diff [PATH...]",
		Short: "Show managed asset changes pending for the linked repository",
		Long: `Compare managed assets in the project workspace with the managed checkout.
The default output may contain complete asset content; do not paste it into
public issues or logs.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			instance, err := buildApp(command, root)
			if err != nil {
				return err
			}
			return instance.Diff(command.Context(), app.DiffOptions{Paths: args, NameOnly: nameOnly, Stat: stat, Staged: staged})
		},
	}
	command.Flags().BoolVar(&nameOnly, "name-only", false, "show only changed managed paths")
	command.Flags().BoolVar(&stat, "stat", false, "show a diff summary instead of full content")
	command.Flags().BoolVar(&staged, "staged", false, "show changes already prepared in an interrupted sync")
	command.MarkFlagsMutuallyExclusive("name-only", "stat")
	return command
}

func newDoctorCommand(root *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose Git, exclusion, collision, and recovery problems",
		Long:  "Run diagnostics without network access, repairs, or persistent changes.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			instance, err := buildApp(command, root)
			if err != nil {
				return err
			}
			return instance.Doctor(command.Context())
		},
	}
}

func newUnlinkCommand(root *rootOptions) *cobra.Command {
	var keepFiles bool
	var removeFiles bool
	var approveRemoveFiles bool
	var keepClone bool
	var removeClone bool
	var force bool
	command := &cobra.Command{
		Use:   "unlink",
		Short: "Remove this workspace's local SPAS association",
		Long: `Remove SPAS link state, its marked local-exclude block, and merge settings
that still exactly match values SPAS wrote. Workspace files and the managed
checkout are kept unless removal is explicitly requested.`,
		Example: `  spas unlink
  spas unlink --remove-files --approve-remove-files --remove-private-clone --non-interactive`,
		Args: cobra.NoArgs,
		PreRunE: func(command *cobra.Command, args []string) error {
			if approveRemoveFiles && !removeFiles {
				return usageError("--approve-remove-files requires --remove-files")
			}
			if command.Flags().Changed("keep-files") {
				if !keepFiles {
					return usageError("use --remove-files to remove SPAS-managed workspace files")
				}
				if removeFiles {
					return usageError("--keep-files and --remove-files are mutually exclusive")
				}
			}
			if command.Flags().Changed("keep-private-clone") {
				if !keepClone {
					return usageError("use --remove-private-clone to remove the managed checkout")
				}
				if removeClone {
					return usageError("--keep-private-clone and --remove-private-clone are mutually exclusive")
				}
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			instance, err := buildApp(command, root)
			if err != nil {
				return err
			}
			return instance.Unlink(command.Context(), app.UnlinkOptions{
				Force:              force,
				RemoveFiles:        removeFiles,
				ApproveRemoveFiles: approveRemoveFiles,
				RemovePrivateClone: removeClone,
			})
		},
	}
	command.Flags().BoolVar(&keepFiles, "keep-files", true, "keep SPAS-managed workspace files; this is the default")
	command.Flags().BoolVar(&removeFiles, "remove-files", false, "remove SPAS-managed workspace files after confirmation")
	command.Flags().BoolVar(&approveRemoveFiles, "approve-remove-files", false, "explicitly approve --remove-files in a non-interactive command")
	command.Flags().BoolVar(&keepClone, "keep-private-clone", true, "keep the managed checkout; this is the default")
	command.Flags().BoolVar(&removeClone, "remove-private-clone", false, "remove the managed checkout")
	command.Flags().BoolVar(&force, "force", false, "unlink despite pending state in the linked repository")
	return command
}

func newCompletionCommand() *cobra.Command {
	command := &cobra.Command{
		Use:                   "completion [bash|zsh|fish|powershell]",
		Short:                 "Generate shell completion",
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		DisableFlagsInUseLine: true,
		RunE: func(command *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return command.Root().GenBashCompletion(command.OutOrStdout())
			case "zsh":
				return command.Root().GenZshCompletion(command.OutOrStdout())
			case "fish":
				return command.Root().GenFishCompletion(command.OutOrStdout(), true)
			case "powershell":
				return command.Root().GenPowerShellCompletionWithDesc(command.OutOrStdout())
			default:
				return fmt.Errorf("unsupported shell %q", args[0])
			}
		},
	}
	return command
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version and build information",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(command.OutOrStdout(), "spas %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
			return err
		},
	}
}

func buildApp(command *cobra.Command, options *rootOptions) (app.App, error) {
	dirs, err := appdirs.Default()
	if err != nil {
		return app.App{}, err
	}
	repoHint := options.repo
	pathBase := options.repo
	if repoHint == "" {
		repoHint, err = os.Getwd()
		if err != nil {
			return app.App{}, err
		}
		pathBase = repoHint
	} else {
		pathBase, err = filepath.Abs(pathBase)
		if err != nil {
			return app.App{}, err
		}
	}
	nonInteractive := options.nonInteractive || options.json
	prompt := interaction.Detect(command.InOrStdin(), command.ErrOrStderr(), nonInteractive)
	prompt.AssumeYes = options.yes
	git := gitexec.Runner{
		Path: options.gitPath,
		// Git terminal prompts are disabled whenever SPAS itself cannot
		// prompt, including non-TTY runs, so authentication fails
		// deterministically instead of hanging while the link lock is held.
		NonInteractive: !prompt.Interactive,
		Stdin:          command.InOrStdin(),
	}
	if !options.json {
		git.Stdout = command.OutOrStdout()
		git.Stderr = command.ErrOrStderr()
	}
	return app.App{
		Git:      git,
		Store:    linkstate.Store{ConfigDir: dirs.Config, DataDir: dirs.Data},
		Prompt:   prompt,
		Out:      command.OutOrStdout(),
		Err:      command.ErrOrStderr(),
		RepoHint: repoHint,
		PathBase: pathBase,
		JSON:     options.json,
		Provider: githubref.Provider{},
	}, nil
}

func classifyExecutionError(err error) error {
	if _, typed := spaserr.KindOf(err); typed {
		return err
	}
	message := err.Error()
	if strings.HasPrefix(message, "unknown command ") ||
		strings.HasPrefix(message, "unknown shorthand flag:") ||
		strings.HasPrefix(message, "unknown flag:") {
		return spaserr.Wrap(spaserr.KindInvalidUsage, err)
	}
	return err
}

func errorKind(err error) spaserr.Kind {
	if kind, ok := spaserr.KindOf(err); ok {
		return kind
	}
	switch {
	case errors.Is(err, linkstate.ErrNotLinked):
		return spaserr.KindNotLinked
	case errors.Is(err, interaction.ErrDecisionRequired):
		return spaserr.KindDecisionRequired
	case errors.Is(err, app.ErrPrivateMergeConflict):
		return spaserr.KindMergeConflict
	default:
		return spaserr.KindOperation
	}
}

func exitCode(err error) int {
	return int(errorKind(err))
}

func errorCode(err error) string {
	return errorKind(err).Code()
}

type rootOptionsKey struct{}

func validateEnum(name, value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return spaserr.Wrap(
		spaserr.KindInvalidUsage,
		fmt.Errorf("%s must be one of: %s", name, strings.Join(allowed, ", ")),
	)
}

func usageError(format string, args ...any) error {
	return spaserr.Wrap(spaserr.KindInvalidUsage, fmt.Errorf(format, args...))
}
