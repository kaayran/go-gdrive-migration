package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
)

// LoadClient creates an authenticated http.Client for Drive API.
// If token file exists it is reused; otherwise OAuth flow is started
// with a local callback endpoint (opens browser).
func LoadClient(ctx context.Context, credentialsFile, tokenFile string) (*http.Client, error) {
	credBytes, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	cfg, err := google.ConfigFromJSON(credBytes,
		drive.DriveScope, // full access: needed for source reads and target writes
	)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	tok, err := loadToken(tokenFile)
	if err != nil {
		fmt.Println("→ Token not found, starting OAuth flow...")
		tok, err = interactiveAuth(ctx, cfg)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokenFile, tok); err != nil {
			return nil, fmt.Errorf("save token: %w", err)
		}
		fmt.Printf("→ Token saved to %s\n", tokenFile)
	}

	return cfg.Client(ctx, tok), nil
}

func loadToken(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	if err := json.NewDecoder(f).Decode(tok); err != nil {
		return nil, err
	}
	return tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(tok)
}

// interactiveAuth starts a local HTTP server on a free port,
// opens browser with authorization URL, and waits for auth code redirect.
func interactiveAuth(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	// Reserve a free local port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// IMPORTANT: redirect_uri must match the OAuth Client ID settings
	// in Google Cloud Console. Desktop app client allows
	// http://localhost:<any-port> and http://127.0.0.1:<any-port>.
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	state := randomState()
	authURL := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce, // ensures refresh_token is returned
	)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("state mismatch")
			return
		}
		if errStr := r.URL.Query().Get("error"); errStr != "" {
			http.Error(w, errStr, http.StatusBadRequest)
			errCh <- fmt.Errorf("oauth error: %s", errStr)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			errCh <- fmt.Errorf("missing code")
			return
		}
		fmt.Fprint(w, `<html><body style="font-family:sans-serif;text-align:center;padding-top:50px;">
<h2>Authorization successful</h2>
<p>You can close this window and return to the terminal.</p>
</body></html>`)
		codeCh <- code
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)
	defer srv.Shutdown(context.Background())

	fmt.Println("→ Opening browser for authorization...")
	fmt.Println("  If browser does not open, visit this URL manually:")
	fmt.Println("  " + authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("  (could not open browser automatically: %v)\n", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, err
	case code := <-codeCh:
		exchangeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return cfg.Exchange(exchangeCtx, code)
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("auth timeout (5 min)")
	}
}

func randomState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
