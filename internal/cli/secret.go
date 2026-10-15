package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func readSMTPPassword(cmd *cobra.Command, fromStdin bool) (string, error) {
	if fromStdin {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("cannot read SMTP password from stdin: %w", err)
		}
		password := strings.TrimRight(string(data), "\r\n")
		if strings.ContainsAny(password, "\r\n") {
			return "", fmt.Errorf("SMTP password must be a single line")
		}
		if password == "" {
			return "", fmt.Errorf("SMTP password from stdin is empty")
		}
		return password, nil
	}
	if password := os.Getenv("MAILSEND_SMTP_PASSWORD"); password != "" {
		if strings.ContainsAny(password, "\r\n") {
			return "", fmt.Errorf("MAILSEND_SMTP_PASSWORD must be a single line")
		}
		return password, nil
	}
	reader := cmd.InOrStdin()
	file, ok := reader.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", fmt.Errorf("SMTP password required: set MAILSEND_SMTP_PASSWORD, pass --password-stdin, or run mailsend account add in a terminal")
	}
	fmt.Fprint(os.Stderr, "SMTP password: ")
	entered, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("cannot read SMTP password: %w", err)
	}
	password := string(entered)
	if password == "" {
		return "", fmt.Errorf("SMTP password is empty")
	}
	return password, nil
}
