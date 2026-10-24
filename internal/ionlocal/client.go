// Package ionlocal reads Instant On switches in local management mode
// through their own web interface. The 1830/1930/1960 have no SSH CLI; the
// web pages are built on an XML interface (wcd?{Section}) that this package
// signs in to and reads, the same way the switch's own pages do.
package ionlocal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

var (
	ErrLogin    = errors.New("the switch didn't accept that username and password")
	ErrTooMany  = errors.New("the switch has too many web sessions open; close its web page in other browsers, or wait ten minutes for old sessions to time out")
	ErrNotLocal = errors.New("this doesn't look like an Instant On switch in local management mode")
	ErrSession  = errors.New("the web session ended")
)

type Client struct {
	Host string // address, optionally with :port
	TLS  bool   // https instead of http
	HTTP *http.Client
	base string // "/cs1a2b3c4d/hpe/"
}

// New returns a client for host. Switches use self-signed certificates, so
// HTTPS certificates aren't checked; the password is RSA-encrypted anyway.
func New(host string, useTLS bool) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{Host: host, TLS: useTLS, HTTP: &http.Client{
		Jar: jar, Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: nil},
	}}
}

func (c *Client) root() string {
	if c.TLS {
		return "https://" + c.Host
	}
	return "http://" + c.Host
}

func (c *Client) fetch(ctx context.Context, path string) ([]byte, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.root()+path, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return b, resp, err
}

// discover finds the session path prefix the switch redirects to.
func (c *Client) discover(ctx context.Context) error {
	_, resp, err := c.fetch(ctx, "/")
	if err != nil {
		return err
	}
	p := resp.Request.URL.Path // after redirects, e.g. /cs1a2b3c4d/hpe/config/login.htm
	if i := strings.Index(p, "/hpe/"); i >= 0 {
		c.base = p[:i+len("/hpe/")]
		return nil
	}
	if i := strings.Index(p, "/config/"); i >= 0 {
		c.base = p[:i+1]
		return nil
	}
	return ErrNotLocal
}

// Login signs in. Call Logout when done: the switch allows only a few web sessions.
func (c *Client) Login(ctx context.Context, user, pass string) error {
	if err := c.discover(ctx); err != nil {
		return err
	}
	body, _, err := c.fetch(ctx, c.base+"device/wcd?{EncryptionSetting}")
	if err != nil {
		return err
	}
	enc, err := Parse(body)
	if err != nil {
		return ErrNotLocal
	}
	var q string
	if enc.Text("passwEncryptEnable") == "1" {
		key, err := publicKey(enc.Text("rsaPublicKey"))
		if err != nil {
			return fmt.Errorf("reading the switch's login key: %w", err)
		}
		msg := "user=" + uriComponent(user) + "&password=" + uriComponent(pass) + "&ssd=true&token=" + enc.Text("loginToken") + "&"
		ct, err := rsa.EncryptPKCS1v15(rand.Reader, key, []byte(msg))
		if err != nil {
			return fmt.Errorf("the username and password are too long for the switch's login key")
		}
		q = "action=login&cred=" + hex.EncodeToString(ct)
	} else {
		q = "action=login&user=" + uriComponent(user) + "&password=" + uriComponent(pass) + "&ssd=true&"
	}
	body, _, err = c.fetch(ctx, c.base+"config/system.xml?"+q)
	if err != nil {
		return err
	}
	st, err := Parse(body)
	if err != nil {
		return fmt.Errorf("unexpected login answer from the switch")
	}
	switch code := st.Text("statusCode"); code {
	case "0", "10", "13": // ok; ok but weak password; ok but password expires soon
		return nil
	case "4", "1":
		return ErrLogin
	case "5":
		return ErrTooMany
	case "9", "12", "14":
		return fmt.Errorf("the switch wants this account's password changed first; sign in to its web page once and set a new one")
	default:
		return fmt.Errorf("the switch refused the login (code %s: %s)", code, st.Text("statusString"))
	}
}

// Logout ends the web session.
func (c *Client) Logout(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c.fetch(ctx, "/System.xml?action=logout")
}

// Get reads one section, e.g. "Ports" or "SystemGlobalSetting".
func (c *Client) Get(ctx context.Context, section string) (*Node, error) {
	body, _, err := c.fetch(ctx, c.base+"wcd?{"+section+"}")
	if err != nil {
		return nil, err
	}
	n, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", section, err)
	}
	if st := n.Find("ActionStatus"); st != nil {
		if code := st.Text("statusCode"); code == "4" || code == "3" {
			return nil, ErrSession
		}
	}
	sec := n.Find(section)
	if sec == nil {
		return nil, fmt.Errorf("the switch has no %s section", section)
	}
	return sec, nil
}

func publicKey(p string) (*rsa.PublicKey, error) {
	p = strings.TrimSpace(p)
	var der []byte
	if b, _ := pem.Decode([]byte(p)); b != nil {
		der = b.Bytes
	} else {
		s := strings.NewReplacer("-----BEGIN RSA PUBLIC KEY-----", "", "-----END RSA PUBLIC KEY-----", "", "-----BEGIN PUBLIC KEY-----", "", "-----END PUBLIC KEY-----", "", "\n", "", "\r", "", " ", "").Replace(p)
		var err error
		if der, err = base64.StdEncoding.DecodeString(s); err != nil {
			return nil, err
		}
	}
	if k, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return rk, nil
}

// uriComponent escapes like JavaScript's encodeURIComponent, which is what
// the switch's login page uses.
func uriComponent(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// Node is a parsed XML element.
type Node struct {
	Name     string
	Value    string
	Children []*Node
}

func Parse(b []byte) (*Node, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	root := &Node{}
	stack := []*Node{root}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &Node{Name: t.Name.Local}
			top := stack[len(stack)-1]
			top.Children = append(top.Children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			stack[len(stack)-1].Value += string(t)
		}
	}
	if len(root.Children) == 0 {
		return nil, errors.New("not XML")
	}
	return root, nil
}

// Find returns the first descendant (or self) named name.
func (n *Node) Find(name string) *Node {
	if n == nil {
		return nil
	}
	if n.Name == name {
		return n
	}
	for _, c := range n.Children {
		if f := c.Find(name); f != nil {
			return f
		}
	}
	return nil
}

// All returns every descendant named name, not looking inside matches.
func (n *Node) All(name string) []*Node {
	var out []*Node
	var walk func(*Node)
	walk = func(x *Node) {
		for _, c := range x.Children {
			if c.Name == name {
				out = append(out, c)
				continue
			}
			walk(c)
		}
	}
	if n != nil {
		walk(n)
	}
	return out
}

// Text returns the trimmed text of the first descendant named name.
func (n *Node) Text(name string) string {
	if f := n.Find(name); f != nil {
		return strings.TrimSpace(f.Value)
	}
	return ""
}
