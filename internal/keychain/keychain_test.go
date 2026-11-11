package keychain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSecurity writes a shell script that mimics the parts of `security` that
// the package uses. Items live in files under dir/items, and every argv is
// appended to dir/argv.log.
func fakeSecurity(t *testing.T) (bin, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "security")
	argvLog = filepath.Join(dir, "argv.log")
	script := `#!/bin/bash
DIR="$(dirname "$0")"
mkdir -p "$DIR/items"
echo "$*" >> "$DIR/argv.log"
run() {
  local cmd="$1"; shift
  local s="" a="" w="" wset=0
  while [ $# -gt 0 ]; do
    case "$1" in
      -s) s="$2"; shift 2;;
      -a) a="$2"; shift 2;;
      -w) if [ $# -ge 2 ]; then w="$2"; wset=1; shift 2; else shift; fi;;
      *) shift;;
    esac
  done
  f="$DIR/items/$s.$a"
  case "$cmd" in
    add-generic-password) printf '%s' "$w" > "$f";;
    find-generic-password)
      if [ ! -f "$f" ]; then echo "security: SecItemCopyMatching: The specified item could not be found in the keychain." >&2; return 44; fi
      cat "$f"; echo;;
    delete-generic-password)
      if [ ! -f "$f" ]; then echo "security: SecItemDelete: The specified item could not be found in the keychain." >&2; return 44; fi
      rm "$f";;
  esac
}
if [ "$1" = "-i" ]; then
  while IFS= read -r line; do
    eval "set -- $line"
    run "$@"
  done
  exit 0
fi
run "$@"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvLog
}

func TestRoundTrip(t *testing.T) {
	bin, argvLog := fakeSecurity(t)
	c := Client{Bin: bin}
	ctx := context.Background()
	secrets := []string{"123456:ABC-def", `we"ird \ sec ret`, "-leading dash"}
	for _, secret := range secrets {
		if err := c.Set(ctx, "rex-tg-test", "bot-token", secret); err != nil {
			t.Fatalf("Set(%q): %v", secret, err)
		}
		got, err := c.Get(ctx, "rex-tg-test", "bot-token")
		if err != nil || got != secret {
			t.Fatalf("Get = %q, %v; want %q", got, err, secret)
		}
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(logged), secret) {
			t.Fatalf("secret %q appears in argv log:\n%s", secret, logged)
		}
	}
	if !strings.Contains(string(logged), "-i") {
		t.Fatalf("Set did not use security -i:\n%s", logged)
	}
	if err := c.Delete(ctx, "rex-tg-test", "bot-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "rex-tg-test", "bot-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := c.Delete(ctx, "rex-tg-test", "bot-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestSetRejectsBadSecrets(t *testing.T) {
	bin, _ := fakeSecurity(t)
	c := Client{Bin: bin}
	for _, s := range []string{"", "a\nb"} {
		if err := c.Set(context.Background(), "s", "a", s); err == nil {
			t.Errorf("Set(%q) succeeded", s)
		}
	}
}

func TestMissingBinary(t *testing.T) {
	c := Client{Bin: filepath.Join(t.TempDir(), "nope")}
	if _, err := c.Get(context.Background(), "s", "a"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get with missing binary = %v", err)
	}
}
