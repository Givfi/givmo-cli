package cmd

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/givfi/givmo-cli/internal/output"
	"github.com/spf13/cobra"
)

var (
	listenForwardTo string
	listenPort      int
)

// DevWebhookSecret is the LOCAL-DEV webhook signing secret the forwarder prints
// so a developer can verify signatures locally. It is NOT a money credential and
// NOT a production secret — it only signs dev-loop test events, so printing it
// is safe and intentional.
const DevWebhookSecret = "whsec_dev_local_givmo_forwarder"

func newListenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "listen --forward-to <host:port>",
		Short: "Forward Connect-sandbox webhooks to a local server",
		Long: `Run a local webhook forwarder against the Connect sandbox. Sandbox test events
are received and forwarded (with a signature header) to your local server at
--forward-to.

It prints the DEV webhook signing secret so you can verify signatures locally.
That secret is a local-dev value only — never a money credential.

Structurally complete; end-to-end delivery is verified when the Connect sandbox
webhook surface goes live (ready-inert). Until then, the local forwarding server
still runs so you can test your handler with 'givmo trigger'.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			app, err := resolveAppCtx()
			if err != nil {
				return err
			}
			if strings.TrimSpace(listenForwardTo) == "" {
				return output.New(output.ExitUsage, "--forward-to is required",
					"Pass --forward-to host:port, e.g. --forward-to localhost:4000.")
			}
			if _, _, err := net.SplitHostPort(listenForwardTo); err != nil {
				return output.New(output.ExitUsage, "invalid --forward-to target: "+err.Error(),
					"Use host:port form, e.g. localhost:4000.")
			}
			return runListen(c.Context(), app)
		},
	}
	cmd.Flags().StringVar(&listenForwardTo, "forward-to", "", "local host:port to forward webhook events to (required)")
	cmd.Flags().IntVar(&listenPort, "port", 4020, "local port the forwarder listens on for sandbox delivery")
	return cmd
}

func runListen(parent context.Context, app *appCtx) error {
	if parent == nil {
		parent = context.Background()
	}
	target := listenForwardTo

	// The forwarder listens locally; the sandbox posts events here once live.
	addr := fmt.Sprintf("127.0.0.1:%d", listenPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return output.New(output.ExitNetwork, "could not bind forwarder: "+err.Error(),
			"Choose a free --port.")
	}

	fmt.Fprintf(app.Printer.Err, "Forwarding sandbox webhooks -> http://%s\n", target)
	fmt.Fprintf(app.Printer.Err, "Listening on http://%s\n", addr)
	fmt.Fprintf(app.Printer.Err, "Dev webhook signing secret: %s\n", DevWebhookSecret)
	fmt.Fprintf(app.Printer.Err, "(sandbox endpoint: %s — delivery is live once the Connect sandbox is enabled)\n", app.Profile.Endpoints.APIBase)

	forwarder := &webhookForwarder{target: target, client: &http.Client{Timeout: 15 * time.Second}, errw: app.Printer.Err}
	srv := &http.Server{Handler: forwarder, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-parent.Done()
		_ = srv.Close()
	}()
	if serr := srv.Serve(ln); serr != nil && serr != http.ErrServerClosed {
		return output.New(output.ExitNetwork, "forwarder stopped: "+serr.Error(), "")
	}
	return nil
}

// webhookForwarder re-signs and forwards received events to the local target.
type webhookForwarder struct {
	target string
	client *http.Client
	errw   io.Writer
}

func (f *webhookForwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	sig := signWebhook(DevWebhookSecret, body)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://"+f.target+r.URL.Path, bytes.NewReader(body))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Givmo-Signature", sig)
		if resp, derr := f.client.Do(req); derr == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			fmt.Fprintf(f.errw, "forwarded %d bytes -> %s (%d)\n", len(body), f.target, resp.StatusCode)
		} else {
			fmt.Fprintf(f.errw, "forward error: %v\n", derr)
		}
	}
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, `{"ok":true}`)
}

// signWebhook computes the dev HMAC-SHA256 signature over the raw body.
// Exported logic (via SignWebhook) so tests can assert the format.
func signWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// SignWebhook is the test-visible signer for the dev webhook signature.
func SignWebhook(secret string, body []byte) string { return signWebhook(secret, body) }
