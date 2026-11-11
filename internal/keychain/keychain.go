// Package keychain stores the bot token in the macOS Keychain through the
// `security` command line tool.
//
// The secret never appears in the arguments of a process: Set sends the
// command to `security -i` on standard input.
package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotFound means the Keychain has no such item.
var ErrNotFound = errors.New("keychain: item not found")

// DefaultBin is the security tool that the package-level functions run.
const DefaultBin = "security"

// Client runs a security binary. The zero value runs DefaultBin.
type Client struct {
	// Bin is the path of the security tool. Empty means DefaultBin.
	Bin string
}

func (c Client) bin() string {
	if c.Bin == "" {
		return DefaultBin
	}
	return c.Bin
}

// Get returns the secret of a generic password item.
func Get(ctx context.Context, service, account string) (string, error) {
	return Client{}.Get(ctx, service, account)
}

// Set stores a secret, replacing an existing item.
func Set(ctx context.Context, service, account, secret string) error {
	return Client{}.Set(ctx, service, account, secret)
}

// Delete removes an item.
func Delete(ctx context.Context, service, account string) error {
	return Client{}.Delete(ctx, service, account)
}

// Get returns the secret of a generic password item. A missing item gives
// ErrNotFound.
func (c Client) Get(ctx context.Context, service, account string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), "find-generic-password", "-s", service, "-a", account, "-w")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if notFound(err, stderr.String()) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain: read %s/%s: %w: %s", service, account, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

// Set stores a secret through `security -i`, so the secret stays out of argv.
// It reads the item back to make sure the write worked.
func (c Client) Set(ctx context.Context, service, account, secret string) error {
	if secret == "" {
		return errors.New("keychain: empty secret")
	}
	if strings.ContainsAny(secret, "\r\n\x00") || strings.ContainsAny(service+account, "\r\n\x00") {
		return errors.New("keychain: values must not contain line breaks")
	}
	line := "add-generic-password -U -s " + quote(service) + " -a " + quote(account) + " -w " + quote(secret) + "\n"
	cmd := exec.CommandContext(ctx, c.bin(), "-i")
	cmd.Stdin = strings.NewReader(line)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain: write %s/%s: %w", service, account, err)
	}
	got, err := c.Get(ctx, service, account)
	if err != nil {
		return fmt.Errorf("keychain: write %s/%s did not take effect: %w", service, account, err)
	}
	if got != secret {
		return fmt.Errorf("keychain: write %s/%s did not take effect", service, account)
	}
	return nil
}

// Delete removes an item. A missing item gives ErrNotFound.
func (c Client) Delete(ctx context.Context, service, account string) error {
	cmd := exec.CommandContext(ctx, c.bin(), "delete-generic-password", "-s", service, "-a", account)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if notFound(err, stderr.String()) {
			return ErrNotFound
		}
		return fmt.Errorf("keychain: delete %s/%s: %w: %s", service, account, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func notFound(err error, stderr string) bool {
	if strings.Contains(stderr, "could not be found") {
		return true
	}
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 44
}

// quote wraps s in double quotes for the `security -i` parser.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
