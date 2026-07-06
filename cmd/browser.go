package cmd

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openBrowser opens url in the user's default browser. It is best-effort:
// callers that fail to open the browser should print the URL for the user to
// open manually. This never blocks the CLI on the browser process.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default: // linux, bsd, etc.
		cmd = "xdg-open"
		args = []string{url}
	}
	if _, err := exec.LookPath(cmd); err != nil {
		return fmt.Errorf("no browser opener (%s) available on this platform", cmd)
	}
	return exec.Command(cmd, args...).Start()
}
