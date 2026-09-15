package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// completionInstallCmd appends the completion hook to a shell rc file.
// Cobra ships a `completion` command that PRINTS a script, which still
// leaves the user to work out where it goes; this does the last step.
//
// Idempotent: it looks for the line before appending, so running it twice
// does not duplicate it.
func completionInstallCmd(binary string) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "install [bash|zsh]",
		Short: "add shell completion to your shell config",
		// OnlyValidArgs so an unsupported shell is a usage error rather
		// than something the switch below has to restate.
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"bash", "zsh"},
		RunE: func(_ *cobra.Command, args []string) error {
			return installCompletion(binary, args[0], file)
		},
	}
	// Plenty of setups do not use ~/.zshrc - a sourced fragment,
	// ~/.zprofile, or $ZDOTDIR elsewhere - so let the caller say where.
	cmd.Flags().StringVar(&file, "file", "", "append to this file instead of the shell's default rc")
	return cmd
}

func installCompletion(binary, shell, file string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home directory: %w", err)
	}

	var rc, line string
	switch shell {
	case "zsh":
		rc = filepath.Join(home, ".zshrc")
		line = fmt.Sprintf("source <(%s completion zsh)", binary)
	case "bash":
		rc = filepath.Join(home, ".bashrc")
		line = fmt.Sprintf("source <(%s completion bash)", binary)
	default:
		return fmt.Errorf("unsupported shell %q (use bash or zsh)", shell)
	}

	if file != "" {
		rc = file
		if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
			return fmt.Errorf("create directory for %s: %w", rc, err)
		}
	}

	if b, err := os.ReadFile(rc); err == nil && strings.Contains(string(b), line) {
		fmt.Printf("completion already installed in %s\n", rc)
		return nil
	}

	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", rc, err)
	}
	defer f.Close()
	if _, err := f.WriteString(fmt.Sprintf("\n# %s shell completion\n%s\n", binary, line)); err != nil {
		return fmt.Errorf("write %s: %w", rc, err)
	}

	fmt.Printf("completion installed in %s\n", rc)
	fmt.Printf("restart your shell, or run: source %s\n", rc)
	return nil
}
