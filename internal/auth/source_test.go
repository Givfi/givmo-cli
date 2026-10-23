package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTokenServer mimics the Givmo authorization server's refresh grant: every
// refresh token may be presented once and is rotated on use, and presenting one
// that is already spent revokes the whole sign-in, every successor included, as
// the server does (plan §2.10). hold, when set, keeps the first renewal open until
// a second renewal arrives or 500 ms pass, so a second renewal racing the first
// is certain to arrive while it is in flight.
type fakeTokenServer struct {
	mu        sync.Mutex
	live      map[string]bool
	spent     map[string]bool
	revoked   bool
	next      int
	refreshes int
	hold      bool
	arrived   chan struct{} // one send per renewal received
	second    chan struct{} // closed when a second renewal arrives
	forms     []url.Values
	auths     []string
}

func newFakeTokenServer(t *testing.T, liveRefreshToken string) (*fakeTokenServer, *httptest.Server) {
	t.Helper()
	f := &fakeTokenServer{
		live:    map[string]bool{liveRefreshToken: true},
		spent:   map[string]bool{},
		arrived: make(chan struct{}, 16),
		second:  make(chan struct{}),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		n, hold, status, body := f.claim(r)
		f.arrived <- struct{}{}
		switch {
		case n == 1 && hold:
			select {
			case <-f.second:
			case <-time.After(500 * time.Millisecond):
			}
		case n == 2:
			close(f.second)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// claim records one renewal and decides its answer at once, as the server's
// claim does before it answers. It returns the renewal's number, whether to hold
// it, and its answer.
func (f *fakeTokenServer) claim(r *http.Request) (int, bool, int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	f.forms = append(f.forms, r.PostForm)
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	rt := r.PostForm.Get("refresh_token")
	if f.revoked || f.spent[rt] || !f.live[rt] {
		f.revoked = f.revoked || f.spent[rt]
		return f.refreshes, f.hold, http.StatusBadRequest, `{"error":"invalid_grant","error_description":"The refresh token is invalid."}`
	}
	delete(f.live, rt)
	f.spent[rt] = true
	f.next++
	successor := fmt.Sprintf("test-refresh-%d", f.next)
	f.live[successor] = true
	return f.refreshes, f.hold, http.StatusOK, fmt.Sprintf(
		`{"access_token":"test-access-%d","token_type":"Bearer","refresh_token":%q,"expires_in":1800,"scope":"givmo.donations.read"}`,
		f.next, successor)
}

func (f *fakeTokenServer) holdFirst() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold = true
}

func (f *fakeTokenServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshes
}

// newSourceStore returns a file-backed store over one temporary GIVMO_HOME;
// stores made after it in the same test share that home, as two processes do.
func newSourceStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("GIVMO_HOME", t.TempDir())
	t.Setenv("GIVMO_TOKEN_BACKEND", "file")
	t.Setenv("GIVMO_CLIENT_SECRET", "")
	return mustStore(t)
}

func mustStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// signIn is a renewable public-client sign-in whose access token expires in
// expiresIn, granted grantedAgo ago.
func signIn(tokenEndpoint string, expiresIn, grantedAgo time.Duration) *Credential {
	return &Credential{
		Profile:            "sandbox",
		Type:               CredTypeOAuth,
		AccessToken:        "test-access-0",
		RefreshToken:       "test-refresh-0",
		TokenType:          "Bearer",
		ExpiresAt:          time.Now().Add(expiresIn),
		ClientID:           "https://mcp.example.test/connect/oauth/clients/givmo-cli",
		Issuer:             "https://mcp.example.test",
		TokenEndpoint:      tokenEndpoint,
		RevocationEndpoint: "https://mcp.example.test/connect/oauth/revoke",
		AuthMethod:         AuthMethodNone,
		GrantedAt:          time.Now().Add(-grantedAgo),
	}
}

func saveSignIn(t *testing.T, s *Store, c *Credential) {
	t.Helper()
	if err := s.Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestTokenSource_RenewsOnceAcrossProcesses pins the invariant: this computer
// never presents one refresh token twice. Two token sources over one GIVMO_HOME
// stand for two givmo processes (each takes the lock file through its own
// descriptor, as two processes do); the fake server holds the first renewal open
// long enough for a racing second one to arrive, and revokes the sign-in if it
// does. The third row gives the two processes different GIVMO_HOME values over one
// keychain sign-in.
func TestTokenSource_RenewsOnceAcrossProcesses(t *testing.T) {
	t.Run("two processes renewing an expiring sign-in renew it once", func(t *testing.T) {
		f, srv := newFakeTokenServer(t, "test-refresh-0")
		f.holdFirst()
		store := newSourceStore(t)
		saveSignIn(t, store, signIn(srv.URL, 10*time.Second, time.Hour))
		a := NewTokenSource(store, "sandbox", srv.Client(), nil)
		b := NewTokenSource(mustStore(t), "sandbox", srv.Client(), nil)

		type result struct {
			cred *Credential
			err  error
		}
		results := make(chan result, 2)
		go func() { c, err := a.Credential(context.Background()); results <- result{c, err} }()
		<-f.arrived // a's renewal is in flight; b reads the same expiring sign-in now
		go func() { c, err := b.Credential(context.Background()); results <- result{c, err} }()
		for i := 0; i < 2; i++ {
			r := <-results
			if r.err != nil {
				t.Fatalf("Credential: %v", r.err)
			}
			if r.cred.AccessToken != "test-access-1" {
				t.Errorf("a caller holds %q, want the renewed test-access-1", r.cred.AccessToken)
			}
		}
		if n := f.count(); n != 1 {
			t.Errorf("the sign-in was renewed %d times, want once", n)
		}
		stored, err := store.Load("sandbox")
		if err != nil {
			t.Fatal(err)
		}
		if stored.RefreshToken != "test-refresh-1" {
			t.Errorf("stored refresh token = %q, want the successor test-refresh-1", stored.RefreshToken)
		}
	})

	t.Run("a sign-out during a renewal leaves nothing stored", func(t *testing.T) {
		f, srv := newFakeTokenServer(t, "test-refresh-0")
		f.holdFirst()
		store := newSourceStore(t)
		saveSignIn(t, store, signIn(srv.URL, 10*time.Second, time.Hour))
		a := NewTokenSource(store, "sandbox", srv.Client(), nil)
		b := NewTokenSource(mustStore(t), "sandbox", srv.Client(), nil)

		renewed := make(chan error, 1)
		go func() { _, err := a.Credential(context.Background()); renewed <- err }()
		<-f.arrived
		var ended *Credential
		if err := b.SignOut(context.Background(), func(c *Credential) { ended = c }); err != nil {
			t.Fatalf("SignOut: %v", err)
		}
		if err := <-renewed; err != nil {
			t.Fatalf("Credential: %v", err)
		}
		if _, err := store.Load("sandbox"); !errors.Is(err, ErrNoCredential) {
			t.Errorf("after the sign-out a credential is stored again (Load: %v)", err)
		}
		if ended == nil || ended.RefreshToken != "test-refresh-1" {
			t.Errorf("the sign-out ended %+v, want the renewed sign-in", ended)
		}
	})

	// A keychain sign-in is one item per user, whatever GIVMO_HOME says: a process
	// whose GIVMO_HOME differs (a shell that exports it, an IDE that does not) reads
	// and renews the same sign-in, so it must wait for the same lock.
	t.Run("two GIVMO_HOME values sharing one keychain sign-in renew it once", func(t *testing.T) {
		trapped := storingFakeKeychain(t)
		f, srv := newFakeTokenServer(t, "test-refresh-0")
		f.holdFirst()
		t.Setenv("GIVMO_HOME", t.TempDir())
		storeA := mustStore(t)
		t.Setenv("GIVMO_HOME", t.TempDir())
		storeB := mustStore(t)
		if storeA.Backend() != "keychain" || storeB.Backend() != "keychain" {
			t.Fatalf("backends %s and %s, want keychain", storeA.Backend(), storeB.Backend())
		}
		saveSignIn(t, storeA, signIn(srv.URL, 10*time.Second, time.Hour))
		// The other process reads it back through the keychain tool, never through a
		// security on PATH, before the race begins.
		if _, err := storeB.Load("sandbox"); err != nil || trapped() {
			t.Fatalf("the sign-in doesn't read back through the keychain tool (Load: %v; the PATH trap ran: %v)", err, trapped())
		}
		a := NewTokenSource(storeA, "sandbox", srv.Client(), nil)
		b := NewTokenSource(storeB, "sandbox", srv.Client(), nil)

		results := make(chan error, 2)
		go func() { _, err := a.Credential(context.Background()); results <- err }()
		<-f.arrived
		go func() { _, err := b.Credential(context.Background()); results <- err }()
		for i := 0; i < 2; i++ {
			if err := <-results; err != nil {
				t.Errorf("Credential: %v", err)
			}
		}
		if n := f.count(); n != 1 {
			t.Errorf("the sign-in was renewed %d times, want once", n)
		}
		stored, err := storeB.Load("sandbox")
		if err != nil || stored.RefreshToken != "test-refresh-1" {
			t.Errorf("stored sign-in = %v (%v), want the successor test-refresh-1", stored, err)
		}
		if trapped() {
			t.Error("the store ran the security found on PATH; it must run its keychain tool by path alone")
		}
	})
}

// storingFakeKeychain stands in for macOS's keychain tool and the user's keychain:
// a shell script, run in place of /usr/bin/security, that stores each item as a
// file under a temporary directory (reading `security -i`'s hex line, answering
// find and delete). It touches no keychain. HOME is a temporary directory too, so
// nothing the store keeps per user reaches the real one. It returns whether the
// trap useFakeSecurity puts on PATH ran, which it never may.
func storingFakeKeychain(t *testing.T) (trapped func() bool) {
	t.Helper()
	trapped = useFakeSecurity(t, storingFakeSecurity)
	if _, err := os.Stat("/usr/bin/xxd"); err != nil {
		t.Skip("the fake keychain decodes its hex with /usr/bin/xxd")
	}
	t.Setenv("FAKE_KEYCHAIN_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIVMO_TOKEN_BACKEND", "")
	t.Setenv("GIVMO_CLIENT_SECRET", "")
	return trapped
}

const storingFakeSecurity = `#!/bin/sh
dir="$FAKE_KEYCHAIN_DIR"
if [ "$1" = "-i" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    set -- $line
    cmd="$1"; shift
    acct=""; svc=""; hex=""
    while [ $# -gt 0 ]; do
      case "$1" in
        -a) acct="$2"; shift 2;;
        -s) svc="$2"; shift 2;;
        -X) hex="$2"; shift 2;;
        *) shift;;
      esac
    done
    [ "$cmd" = "add-generic-password" ] || exit 1
    item="$dir/$svc.$acct"
    printf '%s' "$hex" | /usr/bin/xxd -r -p > "$item.tmp.$$" && /bin/mv "$item.tmp.$$" "$item" || exit 1
  done
  exit 0
fi
cmd="$1"; shift
acct=""; svc=""
while [ $# -gt 0 ]; do
  case "$1" in
    -a) acct="$2"; shift 2;;
    -s) svc="$2"; shift 2;;
    *) shift;;
  esac
done
item="$dir/$svc.$acct"
case "$cmd" in
  find-generic-password) [ -f "$item" ] || exit 44; /bin/cat "$item"; printf '\n';;
  delete-generic-password) [ -f "$item" ] || exit 44; /bin/rm -f "$item";;
  *) exit 1;;
esac
`

// TestTokenSource_ALockWaitThatEndsIsARetry pins what a renewal that cannot get the
// profile's lock in time says: whichever deadline ends the wait, the caller's own or
// the lock's, it is ErrLockTimeout, and a cancelled wait is ErrLockWaitCanceled. Both
// tell the person to retry; nothing is presented and the stored sign-in is unchanged.
// A bare context error would reach the CLI's catch-all, whose remediation is to log
// out, which ends a working sign-in.
func TestTokenSource_ALockWaitThatEndsIsARetry(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(ctx context.Context) (context.Context, context.CancelFunc)
		want error
	}{
		{name: "the caller's deadline ends the wait", want: ErrLockTimeout,
			stop: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, 100*time.Millisecond)
			}},
		{name: "the caller cancels the wait", want: ErrLockWaitCanceled,
			stop: func(ctx context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(ctx)
				time.AfterFunc(100*time.Millisecond, cancel)
				return ctx, cancel
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFakeTokenServer(t, "test-refresh-0")
			f.holdFirst() // the other process's renewal holds the lock for 500 ms
			store := newSourceStore(t)
			saveSignIn(t, store, signIn(srv.URL, 10*time.Second, time.Hour))
			other := NewTokenSource(store, "sandbox", srv.Client(), nil)
			done := make(chan error, 1)
			go func() { _, err := other.Credential(context.Background()); done <- err }()
			<-f.arrived

			ctx, cancel := tc.stop(context.Background())
			defer cancel()
			cred, err := NewTokenSource(mustStore(t), "sandbox", srv.Client(), nil).Credential(ctx)
			if !errors.Is(err, tc.want) || cred != nil {
				t.Errorf("Credential = %v, %v; want %v", cred, err, tc.want)
			}
			if err := <-done; err != nil {
				t.Fatalf("the other process's renewal: %v", err)
			}
			if n := f.count(); n != 1 {
				t.Errorf("%d renewals, want only the other process's", n)
			}
		})
	}
}

// TestTokenSource_RenewalOutcomes pins what each answer to a renewal does to the
// stored sign-in: only invalid_grant ends it; a refused client, a rate limit, a
// server error or a lost answer leave it for the next attempt; and an answer that
// arrives after the caller's deadline is still taken and stored.
func TestTokenSource_RenewalOutcomes(t *testing.T) {
	closedPort := func(t *testing.T) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		return "http://" + addr + "/token"
	}
	answeringAfter := func(delay time.Duration, status int, header http.Header, body string) func(t *testing.T, seen chan<- *http.Request) string {
		return func(t *testing.T, seen chan<- *http.Request) string {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				seen <- r.Clone(context.Background())
				time.Sleep(delay)
				for k, v := range header {
					w.Header()[k] = v
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprint(w, body)
			}))
			t.Cleanup(srv.Close)
			return srv.URL
		}
	}
	answering := func(status int, header http.Header, body string) func(t *testing.T, seen chan<- *http.Request) string {
		return answeringAfter(0, status, header, body)
	}
	renewedBody := `{"access_token":"test-access-1","token_type":"Bearer","refresh_token":"test-refresh-1","expires_in":1800}`
	cases := []struct {
		name       string
		authMethod string
		endpoint   func(t *testing.T, seen chan<- *http.Request) string
		// deadline, when set, is the caller's remaining time for the renewal.
		deadline   time.Duration
		check      func(t *testing.T, cred *Credential, err error, seen *http.Request)
		wantStored string // the refresh token stored afterwards; "" means none
	}{
		{
			name:     "renewed, public client",
			endpoint: answering(http.StatusOK, nil, renewedBody),
			check: func(t *testing.T, cred *Credential, err error, seen *http.Request) {
				if err != nil || cred.AccessToken != "test-access-1" {
					t.Fatalf("Credential = %+v, %v; want the renewed token", cred, err)
				}
				if got := seen.Header.Get("Authorization"); got != "" {
					t.Errorf("a public client's renewal sent Authorization %q", got)
				}
				want := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"test-refresh-0"},
					"client_id": {"https://mcp.example.test/connect/oauth/clients/givmo-cli"}}
				if seen.PostForm.Encode() != want.Encode() {
					t.Errorf("renewal form = %s, want %s", seen.PostForm.Encode(), want.Encode())
				}
			},
			wantStored: "test-refresh-1",
		},
		{
			name:       "renewed, registered client",
			authMethod: AuthMethodClientSecretBasic,
			endpoint:   answering(http.StatusOK, nil, renewedBody),
			check: func(t *testing.T, cred *Credential, err error, seen *http.Request) {
				if err != nil {
					t.Fatalf("Credential: %v", err)
				}
				want := "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape("gci-test")+":"+url.QueryEscape("test-client-secret")))
				if got := seen.Header.Get("Authorization"); got != want {
					t.Errorf("registered renewal Authorization = %q, want %q", got, want)
				}
			},
			wantStored: "test-refresh-1",
		},
		{
			name:     "invalid_grant ends the sign-in",
			endpoint: answering(http.StatusBadRequest, nil, `{"error":"invalid_grant"}`),
			check: func(t *testing.T, cred *Credential, err error, _ *http.Request) {
				if !errors.Is(err, ErrSignInEnded) || cred != nil {
					t.Errorf("Credential = %+v, %v; want ErrSignInEnded", cred, err)
				}
			},
		},
		{
			name:     "invalid_client keeps it",
			endpoint: answering(http.StatusUnauthorized, nil, `{"error":"invalid_client","error_description":"Client authentication failed."}`),
			check: func(t *testing.T, _ *Credential, err error, _ *http.Request) {
				var te *TokenError
				if !errors.As(err, &te) || te.Code != "invalid_client" {
					t.Errorf("err = %v, want a TokenError invalid_client", err)
				}
			},
			wantStored: "test-refresh-0",
		},
		{
			name:     "a rate limit keeps it",
			endpoint: answering(http.StatusTooManyRequests, http.Header{"Retry-After": {"7"}}, `{"error":{"code":"rate_limited"}}`),
			check: func(t *testing.T, _ *Credential, err error, _ *http.Request) {
				var te *TokenError
				if !errors.As(err, &te) || te.Status != http.StatusTooManyRequests || te.RetryAfter != "7" {
					t.Errorf("err = %v, want a 429 TokenError with Retry-After 7", err)
				}
			},
			wantStored: "test-refresh-0",
		},
		{
			name:     "an unreachable server keeps it",
			endpoint: func(t *testing.T, _ chan<- *http.Request) string { return closedPort(t) },
			check: func(t *testing.T, _ *Credential, err error, _ *http.Request) {
				var tr *TransportError
				if !errors.As(err, &tr) || tr.Sent {
					t.Errorf("err = %v, want an unsent TransportError", err)
				}
			},
			wantStored: "test-refresh-0",
		},
		{
			name:     "a server error keeps it",
			endpoint: answering(http.StatusBadGateway, nil, "<html>Bad Gateway</html>"),
			check: func(t *testing.T, _ *Credential, err error, _ *http.Request) {
				var te *TokenError
				if !errors.As(err, &te) || te.Status != http.StatusBadGateway {
					t.Errorf("err = %v, want a 502 TokenError", err)
				}
			},
			wantStored: "test-refresh-0",
		},
		{
			// Once a refresh token is sent the server may have rotated it, so the
			// CLI waits for the answer however little of the caller's time is left:
			// abandoning it would leave the spent token stored, and the next
			// renewal would end the sign-in.
			name:     "an answer that arrives after the caller's deadline is still stored",
			endpoint: answeringAfter(300*time.Millisecond, http.StatusOK, nil, renewedBody),
			deadline: 100 * time.Millisecond,
			check: func(t *testing.T, cred *Credential, err error, _ *http.Request) {
				if err != nil || cred.AccessToken != "test-access-1" {
					t.Errorf("Credential = %+v, %v; want the renewed token", cred, err)
				}
			},
			wantStored: "test-refresh-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newSourceStore(t)
			requests := make(chan *http.Request, 1)
			c := signIn(tc.endpoint(t, requests), 10*time.Second, time.Hour)
			if tc.authMethod != "" {
				c.AuthMethod = tc.authMethod
				c.ClientID = "gci-test"
				if err := store.SaveClientSecret("sandbox", "test-client-secret"); err != nil {
					t.Fatal(err)
				}
			}
			saveSignIn(t, store, c)
			ctx := context.Background()
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.deadline)
				defer cancel()
			}
			cred, err := NewTokenSource(store, "sandbox", &http.Client{Timeout: 5 * time.Second}, nil).Credential(ctx)
			seen := &http.Request{}
			select {
			case seen = <-requests:
			default:
			}
			tc.check(t, cred, err, seen)
			stored, lerr := store.Load("sandbox")
			switch {
			case tc.wantStored == "" && !errors.Is(lerr, ErrNoCredential):
				t.Errorf("a credential is still stored (Load: %v)", lerr)
			case tc.wantStored != "" && (lerr != nil || stored.RefreshToken != tc.wantStored):
				t.Errorf("stored refresh token = %v (%v), want %q", stored, lerr, tc.wantStored)
			}
			if tc.authMethod != "" {
				if secret, _ := store.LoadClientSecret("sandbox"); secret != "test-client-secret" {
					t.Errorf("the registered client's secret was not kept: %q", secret)
				}
			}
		})
	}
}

// TestTokenSource_SaysWhenTheSignInEnds pins the reminder: in the sign-in's last
// week before its 90-day ceiling, one line says when it ends, once however many
// times the credential is used (the bridge uses it on every request), and not
// before that week.
func TestTokenSource_SaysWhenTheSignInEnds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		grantedAgo time.Duration
		reminded   bool
	}{
		{name: "six days left", grantedAgo: 84 * 24 * time.Hour, reminded: true},
		{name: "eighty days left", grantedAgo: 10 * 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newSourceStore(t)
			c := signIn("https://mcp.example.test/connect/oauth/token", time.Hour, tc.grantedAgo)
			saveSignIn(t, store, c)
			want := ""
			if tc.reminded {
				want = "Your Givmo sign-in on this computer ends on " +
					c.GrantedAt.Add(SignInLifetime).Local().Format("2006-01-02") + ". Run `givmo login` before then.\n"
			}
			var notices bytes.Buffer
			src := NewTokenSource(store, "sandbox", nil, &notices)
			for i := 0; i < 2; i++ {
				if _, err := src.Credential(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if got := notices.String(); got != want {
				t.Errorf("notices = %q, want %q", got, want)
			}
			if strings.Count(notices.String(), "\n") > 1 {
				t.Errorf("the reminder was printed more than once")
			}
		})
	}
}

// answeringTransport answers every request itself, as a token endpoint renewing
// a sign-in would, and records where each was going, so a test can say whether
// any request left at all.
type answeringTransport struct {
	mu   sync.Mutex
	sent []string
}

func (a *answeringTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	a.mu.Lock()
	a.sent = append(a.sent, r.URL.String())
	a.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"access_token":"test-access-1","token_type":"Bearer","refresh_token":"test-refresh-1","expires_in":1800}`)),
		Request:    r,
	}, nil
}

func (a *answeringTransport) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sent...)
}

// TestTokenSource_RefusesAStoredSignInTheRulesRefuse: each time a sign-in is
// loaded, the addresses its login adopted are checked again with the rules a
// login applies (CheckAddress). A sign-in that fails them is not used: no access
// token is handed out, and no request leaves for any of its addresses, its
// refresh token least of all. It stays stored, so a re-login or a logout can say
// it was not ended on Givmo's side. Each sign-in here is due for renewal, so one
// that is used is renewed through its token endpoint at once, except a fresh one,
// whose access token is refused even though no renewal would send anything.
func TestTokenSource_RefusesAStoredSignInTheRulesRefuse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		profile   string
		fresh     bool // its access token is an hour from expiry, so not due
		edit      func(c *Credential)
		wantField string // the refused address; "" when the sign-in is used
		wantErr   error
	}{
		{name: "an http token endpoint off this computer", profile: "sandbox",
			edit:      func(c *Credential) { c.TokenEndpoint = "http://token.example.test/token" },
			wantField: "token endpoint", wantErr: ErrNotHTTPS},
		{name: "a loopback http token endpoint under the production profile", profile: "production",
			edit:      func(c *Credential) { c.TokenEndpoint = "http://127.0.0.1:9/token" },
			wantField: "token endpoint", wantErr: ErrNotHTTPS},
		{name: "a revocation endpoint whose host isn't ASCII", profile: "sandbox",
			edit:      func(c *Credential) { c.RevocationEndpoint = "https://ｍｃｐ.example.test/connect/oauth/revoke" },
			wantField: "revocation endpoint", wantErr: ErrHostNotASCII},
		{name: "an authorization server whose host is percent-encoded non-ASCII", profile: "sandbox",
			edit:      func(c *Credential) { c.Issuer = "https://%EF%BD%8D%EF%BD%83%EF%BD%90.example.test" },
			wantField: "authorization server", wantErr: ErrHostNotASCII},
		{name: "an authorization server that isn't https", profile: "sandbox",
			edit:      func(c *Credential) { c.Issuer = "http://mcp.example.test" },
			wantField: "authorization server", wantErr: ErrNotHTTPS},
		{name: "a fresh access token from a sign-in whose token endpoint isn't https", profile: "sandbox", fresh: true,
			edit:      func(c *Credential) { c.TokenEndpoint = "http://token.example.test/token" },
			wantField: "token endpoint", wantErr: ErrNotHTTPS},
		{name: "https throughout, under the production profile", profile: "production", edit: func(*Credential) {}},
		{name: "a loopback http token endpoint on the sandbox profile", profile: "sandbox",
			edit: func(c *Credential) { c.TokenEndpoint = "http://127.0.0.1:9/token" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newSourceStore(t)
			expiresIn := 10 * time.Second
			if tc.fresh {
				expiresIn = time.Hour
			}
			c := signIn("https://mcp.example.test/connect/oauth/token", expiresIn, time.Hour)
			c.Profile = tc.profile
			tc.edit(c)
			saveSignIn(t, store, c)
			transport := &answeringTransport{}
			src := NewTokenSource(store, tc.profile, &http.Client{Transport: transport}, nil)

			got, err := src.Credential(context.Background())
			if tc.wantField == "" {
				if err != nil || got == nil || got.AccessToken != "test-access-1" {
					t.Fatalf("Credential = %+v, %v; want the sign-in, renewed", got, err)
				}
				if sent := transport.requests(); len(sent) != 1 || sent[0] != c.TokenEndpoint {
					t.Errorf("requests = %q, want one renewal at %s", sent, c.TokenEndpoint)
				}
				return
			}
			_, rejectedErr := src.Rejected(context.Background(), c.AuthorizationHeader())
			for name, err := range map[string]error{"Credential": err, "Rejected": rejectedErr} {
				var ae *StoredAddressError
				if !errors.As(err, &ae) || ae.Field != tc.wantField || !errors.Is(err, tc.wantErr) {
					t.Errorf("%s error = %v, want its %s refused (%v)", name, err, tc.wantField, tc.wantErr)
				}
			}
			if got != nil {
				t.Errorf("Credential handed out %+v", got)
			}
			if sent := transport.requests(); len(sent) != 0 {
				t.Errorf("requests %q left; a sign-in the rules refuse sends nothing", sent)
			}
			if stored, err := store.Load(tc.profile); err != nil || stored.RefreshToken != "test-refresh-0" {
				t.Errorf("stored sign-in = %+v, %v; want it kept as it was", stored, err)
			}
		})
	}
}
