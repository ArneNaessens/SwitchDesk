// Package cxrest uploads firmware images to AOS-CX switches through their
// REST API. AOS-CX has no SFTP or SCP server, so this is the only way to send
// an image to the switch instead of having the switch fetch it.
package cxrest

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Fallback versions tried when GET /rest doesn't say which ones exist.
var knownVersions = []string{"v10.13", "v10.12", "v10.11", "v10.10", "v10.09", "v10.08", "v10.04", "v1"}

// ErrNoREST means nothing answered on HTTPS, or nothing that looks like the REST API.
var ErrNoREST = errors.New("the switch's REST API didn't answer on HTTPS")

type Client struct {
	Base string // https://host[:port]
	HTTP *http.Client
	pre  string // /rest/v10.xx
	csrf string // returned by login on releases that use it
}

// New returns a client for base. Switches use self-signed certificates, so
// the certificate isn't checked; login and image are still sent encrypted.
func New(base string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{
		Jar:       jar,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: nil, ResponseHeaderTimeout: 20 * time.Minute},
	}}
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, ctype string, timeout time.Duration) (*http.Response, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer func() {
			if cancel != nil {
				cancel()
			}
		}()
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	c.headers(req)
	if strings.HasSuffix(path, "/login") {
		req.Header.Set("x-use-csrf-token", "true")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if t := resp.Header.Get("X-Csrf-Token"); t != "" {
		c.csrf = t
	}
	// read the body now so the timeout context can be released
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	resp.Body = io.NopCloser(strings.NewReader(string(data)))
	return resp, err
}

func (c *Client) headers(req *http.Request) {
	req.Header.Set("Accept", "*/*")
	if c.csrf != "" {
		req.Header.Set("x-csrf-token", c.csrf)
	}
}

func bodyText(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	t := strings.TrimSpace(string(b))
	if len(t) > 200 {
		t = t[:200]
	}
	return t
}

// version finds the newest REST version the switch offers.
func (c *Client) version(ctx context.Context) error {
	resp, err := c.do(ctx, "GET", "/rest", nil, "", 20*time.Second)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrNoREST, err)
	}
	var v struct {
		Latest struct {
			Prefix string `json:"prefix"`
		} `json:"latest"`
	}
	if resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&v) == nil && strings.HasPrefix(v.Latest.Prefix, "/rest/") {
		c.pre = v.Latest.Prefix
		return nil
	}
	if resp.StatusCode == 404 || resp.StatusCode == 200 {
		c.pre = "" // unknown; login tries the known versions
		return nil
	}
	return fmt.Errorf("%w (HTTP %d)", ErrNoREST, resp.StatusCode)
}

// Login opens a REST session. Call Logout when done: switches allow only a
// few sessions per user.
func (c *Client) Login(ctx context.Context, user, pass string) error {
	if err := c.version(ctx); err != nil {
		return err
	}
	try := knownVersions
	if c.pre != "" {
		try = []string{strings.TrimPrefix(c.pre, "/rest/")}
	}
	form := url.Values{"username": {user}, "password": {pass}}.Encode()
	var last string
	for _, v := range try {
		resp, err := c.do(ctx, "POST", "/rest/"+v+"/login", strings.NewReader(form), "application/x-www-form-urlencoded", 30*time.Second)
		if err != nil {
			return fmt.Errorf("%w (%v)", ErrNoREST, err)
		}
		switch resp.StatusCode {
		case 200, 201, 204:
			c.pre = "/rest/" + v
			return nil
		case 401, 403:
			return fmt.Errorf("the switch's REST API refused the login (HTTP %d). The SSH account needs administrator rights", resp.StatusCode)
		case 429, 503:
			return fmt.Errorf("the switch has too many REST sessions open (HTTP %d); wait a few minutes or log out of its web page", resp.StatusCode)
		}
		last = fmt.Sprintf("HTTP %d %s", resp.StatusCode, bodyText(resp))
	}
	return fmt.Errorf("%w: login failed (%s)", ErrNoREST, last)
}

func (c *Client) Logout(ctx context.Context) {
	if c.pre == "" {
		return
	}
	c.do(ctx, "POST", c.pre+"/logout", nil, "", 15*time.Second)
}

// Upload sends the image to bank and returns once the switch has written and
// checked it. progress reports bytes sent.
func (c *Client) Upload(ctx context.Context, bank, name string, file io.Reader, size int64, progress func(sent, total int64)) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="fileupload"; filename=%q`, filepath.Base(name)))
		h.Set("Content-Type", "application/octet-stream")
		part, err := mw.CreatePart(h)
		if err == nil {
			_, err = io.Copy(part, &counter{r: file, total: size, fn: progress})
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, "POST", c.Base+c.pre+"/firmware?image="+url.QueryEscape(bank), pr)
	if err != nil {
		return err
	}
	c.headers(req)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("uploading over HTTPS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("the switch didn't accept the image (HTTP %d): %s", resp.StatusCode, bodyText(resp))
	}
	return nil
}

type counter struct {
	r     io.Reader
	sent  int64
	total int64
	last  time.Time
	fn    func(sent, total int64)
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.sent += int64(n)
	if c.fn != nil && (time.Since(c.last) > 300*time.Millisecond || err == io.EOF) {
		c.last = time.Now()
		c.fn(c.sent, c.total)
	}
	return n, err
}

// Push logs in, uploads and logs out.
func Push(ctx context.Context, base, user, pass, bank, name string, file io.Reader, size int64, progress func(sent, total int64)) error {
	c := New(base)
	if err := c.Login(ctx, user, pass); err != nil {
		return err
	}
	defer c.Logout(context.Background())
	return c.Upload(ctx, bank, name, file, size, progress)
}
