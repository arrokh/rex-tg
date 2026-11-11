package install

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launchdPath = "/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin"

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// plist renders the launchd agent definition.
func (p Paths) plist() []byte {
	var b bytes.Buffer
	str := func(indent, key, val string) {
		fmt.Fprintf(&b, "%s<key>%s</key>\n%s<string>%s</string>\n", indent, key, indent, xmlEscape(val))
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	str("\t", "Label", p.Label)
	fmt.Fprintf(&b, "\t<key>ProgramArguments</key>\n\t<array>\n\t\t<string>%s</string>\n\t\t<string>daemon</string>\n\t</array>\n", xmlEscape(p.Binary))
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>KeepAlive</key>\n\t<true/>\n")
	str("\t", "ProcessType", "Standard")
	str("\t", "StandardOutPath", filepath.Join(p.LogDir, "launchd.out.log"))
	str("\t", "StandardErrorPath", filepath.Join(p.LogDir, "launchd.err.log"))
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	str("\t\t", "HOME", p.Home)
	str("\t\t", "PATH", launchdPath)
	b.WriteString("\t</dict>\n</dict>\n</plist>\n")
	return b.Bytes()
}

func (p Paths) domainTarget() string { return "gui/" + p.uid() }

// notLoaded reports whether a failed bootout means the agent was not loaded.
func notLoaded(out []byte, err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) && (ee.ExitCode() == 3 || ee.ExitCode() == 113) {
		return true
	}
	s := strings.ToLower(string(out) + " " + err.Error())
	for _, m := range []string{"no such process", "could not find", "not found", "not loaded"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func (p Paths) bootout() error {
	out, err := p.run("launchctl", "bootout", p.domainTarget()+"/"+p.Label)
	if err != nil && !notLoaded(out, err) {
		return fmt.Errorf("launchctl bootout: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (p Paths) bootstrap() error {
	out, err := p.run("launchctl", "bootstrap", p.domainTarget(), p.LaunchAgent)
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// InstallLaunchd writes the launchd agent plist. With load, it then runs
// bootout (ignoring "not loaded") and bootstrap when the plist changed. When
// the plist is unchanged it only bootstraps if `launchctl print` says the
// agent is not loaded, so a second install does not restart the daemon.
func InstallLaunchd(p Paths, load bool) (Change, error) {
	if err := p.validate(); err != nil {
		return Change{Path: p.LaunchAgent}, err
	}
	if err := os.MkdirAll(p.LogDir, 0o755); err != nil {
		return Change{Path: p.LaunchAgent}, fmt.Errorf("create log folder: %w", err)
	}
	ch, err := p.installFile(p.LaunchAgent, p.plist(), 0o644)
	if err != nil || !load {
		return ch, err
	}
	if !ch.Changed {
		if _, perr := p.run("launchctl", "print", p.domainTarget()+"/"+p.Label); perr == nil {
			return ch, nil
		}
	}
	if err := p.bootout(); err != nil {
		return ch, err
	}
	if err := p.bootstrap(); err != nil {
		return ch, err
	}
	ch.Note += "; loaded"
	return ch, nil
}

// UninstallLaunchd optionally unloads the agent, then removes the plist.
func UninstallLaunchd(p Paths, unload bool) (Change, error) {
	if unload {
		if err := p.bootout(); err != nil {
			return Change{Path: p.LaunchAgent}, err
		}
	}
	return removeFile(p.LaunchAgent)
}
