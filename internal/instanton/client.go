// Package instanton reads Aruba Instant On devices from the Instant On
// portal. Instant On switches managed from the cloud have no SSH or Telnet,
// so the portal is the only way to see them.
//
// The portal API isn't documented by HPE. Login is the same OAuth flow the
// portal's own web page uses, and the endpoints are the ones the page calls.
// Accounts with two-step verification can't log in this way.
package instanton

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultPortal is where the portal publishes its settings (SSO host, API base).
const DefaultPortal = "https://portal.instant-on.hpe.com"

// APIVersion is sent as x-ion-api-version; without it some endpoints fail.
const APIVersion = "7"

var (
	ErrLogin = errors.New("the Instant On portal didn't accept that email and password. If the account uses two-step verification, use a separate account without it (view-only access is enough)")
	ErrAuth  = errors.New("the Instant On login has expired")
)

type settings struct {
	RestAPIURL string `json:"restApiUrl"`
	SSOFqdn    string `json:"ssoFqdn"`
	ClientID   string `json:"ssoClientIdAuthZ"`
	Redirect   string `json:"ssoRedirectUrl"`
}

// Tokens are what a login returns. The refresh token is kept so SwitchDesk
// doesn't send the password on every poll.
type Tokens struct {
	Access  string    `json:"access"`
	Refresh string    `json:"refresh"`
	Expires time.Time `json:"expires"`
}

type Client struct {
	Portal   string
	HTTP     *http.Client
	Email    string // kept in memory so an expired session can log in again
	Password string

	mu  sync.Mutex
	set *settings
	tok Tokens
}

func New(email, password string) *Client {
	return &Client{Portal: DefaultPortal, HTTP: &http.Client{Timeout: 30 * time.Second}, Email: email, Password: password}
}

// ensure makes sure there's a valid access token: refresh it when it's about
// to run out, and log in again when that fails.
func (c *Client) ensure(ctx context.Context) error {
	c.mu.Lock()
	tok := c.tok
	c.mu.Unlock()
	if tok.Access != "" && time.Now().Before(tok.Expires) {
		return nil
	}
	if tok.Refresh != "" {
		if _, err := c.Refresh(ctx, tok.Refresh); err == nil {
			return nil
		}
	}
	if c.Email == "" {
		return ErrAuth
	}
	_, err := c.Login(ctx, c.Email, c.Password)
	return err
}

func (c *Client) settings(ctx context.Context) (*settings, error) {
	c.mu.Lock()
	if c.set != nil {
		s := c.set
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.Portal, "/")+"/settings.json", nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach the Instant On portal: %w", err)
	}
	defer resp.Body.Close()
	var s settings
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&s) != nil || s.SSOFqdn == "" || s.ClientID == "" || s.RestAPIURL == "" {
		return nil, fmt.Errorf("the Instant On portal's settings look different than expected (HTTP %d)", resp.StatusCode)
	}
	if s.Redirect == "" {
		s.Redirect = c.Portal
	}
	c.mu.Lock()
	c.set = &s
	c.mu.Unlock()
	return &s, nil
}

func randURLSafe(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(b), "=")
}

// Login signs in with email and password.
func (c *Client) Login(ctx context.Context, email, password string) (Tokens, error) {
	s, err := c.settings(ctx)
	if err != nil {
		return Tokens{}, err
	}
	// 1. password check, returns a short-lived session token
	form := url.Values{"username": {email}, "password": {password}}
	req, _ := http.NewRequestWithContext(ctx, "POST", s.SSOFqdn+"/aio/api/v1/mfa/validate/full", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("couldn't reach Instant On sign-in: %w", err)
	}
	var sess struct {
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&sess)
	resp.Body.Close()
	if resp.StatusCode == 429 {
		return Tokens{}, errors.New("the Instant On portal is limiting sign-ins right now; try again in a few minutes")
	}
	if resp.StatusCode != 200 || sess.AccessToken == "" {
		return Tokens{}, ErrLogin
	}

	// 2. authorization code with PKCE; the code arrives in a redirect we don't follow
	verifier := randURLSafe(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randURLSafe(24)
	q := url.Values{
		"client_id": {s.ClientID}, "redirect_uri": {s.Redirect}, "response_type": {"code"},
		"scope": {"profile openid"}, "state": {state},
		"code_challenge_method": {"S256"}, "code_challenge": {challenge}, "sessionToken": {sess.AccessToken},
	}
	noRedirect := *c.HTTP
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ = http.NewRequestWithContext(ctx, "GET", s.SSOFqdn+"/as/authorization.oauth2?"+q.Encode(), nil)
	resp, err = noRedirect.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("Instant On sign-in failed: %w", err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := ""
	if loc != nil && loc.Query().Get("state") == state {
		code = loc.Query().Get("code")
	}
	if code == "" {
		return Tokens{}, fmt.Errorf("Instant On sign-in didn't return an authorization code (HTTP %d)", resp.StatusCode)
	}

	// 3. exchange the code for tokens
	return c.token(ctx, s, url.Values{
		"client_id": {s.ClientID}, "redirect_uri": {s.Redirect}, "code": {code},
		"code_verifier": {verifier}, "grant_type": {"authorization_code"},
	})
}

// Refresh gets a new access token from a refresh token.
func (c *Client) Refresh(ctx context.Context, refresh string) (Tokens, error) {
	s, err := c.settings(ctx)
	if err != nil {
		return Tokens{}, err
	}
	t, err := c.token(ctx, s, url.Values{"client_id": {s.ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if err != nil {
		return t, ErrAuth
	}
	if t.Refresh == "" {
		t.Refresh = refresh
	}
	return t, nil
}

func (c *Client) token(ctx context.Context, s *settings, form url.Values) (Tokens, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", s.SSOFqdn+"/as/token.oauth2", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	var t struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t) != nil || t.Access == "" {
		return Tokens{}, fmt.Errorf("Instant On token request failed (HTTP %d)", resp.StatusCode)
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = 1800
	}
	tok := Tokens{Access: t.Access, Refresh: t.Refresh, Expires: time.Now().Add(time.Duration(t.ExpiresIn)*time.Second - time.Minute)}
	c.mu.Lock()
	c.tok = tok
	c.mu.Unlock()
	return tok, nil
}

// Use sets tokens obtained earlier.
func (c *Client) Use(t Tokens) {
	c.mu.Lock()
	c.tok = t
	c.mu.Unlock()
}

func (c *Client) Tokens() Tokens {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tok
}

// get calls a portal API path and decodes JSON into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	s, err := c.settings(ctx)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		if err := c.ensure(ctx); err != nil {
			return err
		}
		err := c.getOnce(ctx, s, path, out)
		if errors.Is(err, ErrAuth) && attempt == 0 {
			c.mu.Lock()
			c.tok.Expires = time.Time{} // token rejected early: refresh or log in again
			c.mu.Unlock()
			continue
		}
		return err
	}
}

func (c *Client) getOnce(ctx context.Context, s *settings, path string, out any) error {
	c.mu.Lock()
	tok := c.tok
	c.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(s.RestAPIURL, "/")+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok.Access)
	req.Header.Set("x-ion-api-version", APIVersion)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach the Instant On portal: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrAuth
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("Instant On portal answered HTTP %d for %s", resp.StatusCode, path)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}
