// Package api is the CLI's client for gears-api and the credentials file
// that holds the device token. One token per API host; the default host
// is the last one logged in to, overridable with GEARS_API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultHost = "https://api.gearsos.com"

// Credentials is ~/.gears/credentials.json.
type Credentials struct {
	Default string                `json:"default"`
	Hosts   map[string]Credential `json:"hosts"`
}

type Credential struct {
	Token     string    `json:"token"`
	Label     string    `json:"label,omitempty"`
	Email     string    `json:"email,omitempty"`
	DeviceID  string    `json:"deviceId,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gears", "credentials.json"), nil
}

func LoadCredentials() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	c := &Credentials{Hosts: map[string]Credential{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Hosts == nil {
		c.Hosts = map[string]Credential{}
	}
	return c, nil
}

func (c *Credentials) Save() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// Host is the API to talk to: GEARS_API, else the saved default, else prod.
func Host() string {
	if h := strings.TrimSpace(os.Getenv("GEARS_API")); h != "" {
		return strings.TrimRight(h, "/")
	}
	if c, err := LoadCredentials(); err == nil && c.Default != "" {
		return c.Default
	}
	return DefaultHost
}

// ErrNotLoggedIn means no token for the host.
var ErrNotLoggedIn = errors.New("not logged in; run: gears login")

// Client is one API host with its token.
type Client struct {
	Host  string
	Token string
	Label string
	httpc *http.Client
}

// Open returns a client for the host using the saved token.
func Open(host string) (*Client, error) {
	host = strings.TrimRight(host, "/")
	creds, err := LoadCredentials()
	if err != nil {
		return nil, err
	}
	cr, ok := creds.Hosts[host]
	if !ok || cr.Token == "" {
		return nil, ErrNotLoggedIn
	}
	if !cr.ExpiresAt.IsZero() && time.Now().After(cr.ExpiresAt) {
		return nil, fmt.Errorf("device token for %s expired; run: gears login", host)
	}
	return &Client{Host: host, Token: cr.Token, Label: cr.Label, httpc: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Anonymous returns a client with no token, for login.
func Anonymous(host string) *Client {
	return &Client{Host: strings.TrimRight(host, "/"), httpc: &http.Client{Timeout: 30 * time.Second}}
}

// Problem is an RFC 7807 body from the API.
type Problem struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func (p *Problem) Error() string {
	if p.Detail != "" {
		return fmt.Sprintf("%s (%d %s)", p.Detail, p.Status, p.Code)
	}
	return fmt.Sprintf("%d %s", p.Status, p.Code)
}

// Do sends a JSON request. out may be nil. 304 is not an error: out stays
// empty and notModified is true.
func (c *Client) Do(ctx context.Context, method, path string, body any, out any) (notModified bool, err error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return false, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Host+path, rdr)
	if err != nil {
		return false, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.auth(req)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return false, fmt.Errorf("%s unreachable: %w", c.Host, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusNotModified {
		return true, nil
	}
	if resp.StatusCode >= 300 {
		var p Problem
		if json.Unmarshal(raw, &p) == nil && p.Status != 0 {
			return false, &p
		}
		return false, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return false, json.Unmarshal(raw, out)
	}
	return false, nil
}

// Raw sends bytes (artifact content) and returns the response body.
func (c *Client) Raw(ctx context.Context, method, path string, body []byte, contentType string) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Host+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.auth(req)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s unreachable: %w", c.Host, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return raw, resp.StatusCode, nil
}

func (c *Client) auth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Label != "" {
		req.Header.Set("X-Gears-Label", c.Label)
	}
}

// ------------------------------------------------------------------ verbs

type LoginOut struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	Device    struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"device"`
}

// Login exchanges credentials for a device token and saves it.
func Login(ctx context.Context, host, email, password, label, hostname string) (*Credential, error) {
	c := Anonymous(host)
	var out LoginOut
	if _, err := c.Do(ctx, http.MethodPost, "/auth/device", map[string]string{"email": email, "password": password, "label": label, "host": hostname}, &out); err != nil {
		return nil, err
	}
	creds, err := LoadCredentials()
	if err != nil {
		return nil, err
	}
	cr := Credential{Token: out.Token, Label: out.Device.Label, Email: email, DeviceID: out.Device.ID, ExpiresAt: out.ExpiresAt}
	creds.Hosts[c.Host] = cr
	creds.Default = c.Host
	if err := creds.Save(); err != nil {
		return nil, err
	}
	return &cr, nil
}

// Logout revokes the device on the server (best effort) and forgets the token.
func Logout(ctx context.Context, host string) (revoked bool, err error) {
	host = strings.TrimRight(host, "/")
	creds, err := LoadCredentials()
	if err != nil {
		return false, err
	}
	cr, ok := creds.Hosts[host]
	if !ok {
		return false, ErrNotLoggedIn
	}
	if cr.DeviceID != "" {
		c := &Client{Host: host, Token: cr.Token, httpc: &http.Client{Timeout: 15 * time.Second}}
		if _, err := c.Do(ctx, http.MethodDelete, "/api/v1/devices/"+cr.DeviceID, nil, nil); err == nil {
			revoked = true
		}
	}
	delete(creds.Hosts, host)
	if creds.Default == host {
		creds.Default = ""
		for h := range creds.Hosts {
			creds.Default = h
		}
	}
	return revoked, creds.Save()
}

type Me struct {
	OrganizationID string `json:"organizationId"`
	TenantID       string `json:"tenantId"`
	ActorID        string `json:"actorId"`
	Label          string `json:"label"`
}

func (c *Client) Me(ctx context.Context) (*Me, error) {
	var m Me
	_, err := c.Do(ctx, http.MethodGet, "/api/v1/me", nil, &m)
	return &m, err
}

type Workspace struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name,omitempty"`
	Prefix    string    `json:"prefix,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type Project struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name,omitempty"`
	GitRemote   string    `json:"gitRemote,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

func (c *Client) UpsertWorkspace(ctx context.Context, w Workspace) (*Workspace, bool, error) {
	var out Workspace
	nm, err := c.Do(ctx, http.MethodPost, "/api/v1/workspaces", w, &out)
	if err != nil {
		return nil, false, err
	}
	if nm {
		return &w, false, nil
	}
	return &out, true, nil
}

func (c *Client) UpsertProject(ctx context.Context, p Project) (*Project, bool, error) {
	var out Project
	nm, err := c.Do(ctx, http.MethodPost, "/api/v1/projects", p, &out)
	if err != nil {
		return nil, false, err
	}
	if nm {
		return &p, false, nil
	}
	return &out, true, nil
}

type BindOut struct {
	Project   Project   `json:"project"`
	Workspace Workspace `json:"workspace"`
}

// Bind asks which project has this git remote. nil, nil when none.
func (c *Client) Bind(ctx context.Context, remote string) (*BindOut, error) {
	var out BindOut
	_, err := c.Do(ctx, http.MethodGet, "/api/v1/projects/bind?remote="+urlQuery(remote), nil, &out)
	var p *Problem
	if errors.As(err, &p) && p.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetWorkspace returns nil, nil when the account has no such workspace.
func (c *Client) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	var out Workspace
	_, err := c.Do(ctx, http.MethodGet, "/api/v1/workspaces/"+id, nil, &out)
	var p *Problem
	if errors.As(err, &p) && p.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProject returns nil, nil when the account has no such project.
func (c *Client) GetProject(ctx context.Context, id string) (*Project, error) {
	var out Project
	_, err := c.Do(ctx, http.MethodGet, "/api/v1/projects/"+id, nil, &out)
	var p *Problem
	if errors.As(err, &p) && p.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListProjects is the account's live projects, in one workspace when ws is
// set.
func (c *Client) ListProjects(ctx context.Context, ws string) ([]Project, error) {
	var out []Project
	path := "/api/v1/projects"
	if ws != "" {
		path += "?workspaceId=" + urlQuery(ws)
	}
	_, err := c.Do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	var out []Workspace
	_, err := c.Do(ctx, http.MethodGet, "/api/v1/workspaces", nil, &out)
	return out, err
}

// Change is one pulled entry.
type Change struct {
	Seq     int64           `json:"seq"`
	Table   string          `json:"table"`
	ID      string          `json:"id"`
	Op      string          `json:"op"`
	By      string          `json:"by,omitempty"`
	At      time.Time       `json:"at"`
	Row     json.RawMessage `json:"row,omitempty"`
	Deleted bool            `json:"deleted,omitempty"`
}

type PullOut struct {
	Cursor  int64    `json:"cursor"`
	Changes []Change `json:"changes"`
	More    bool     `json:"more"`
}

func (c *Client) Pull(ctx context.Context, workspaceID string, cursor int64) (*PullOut, error) {
	var out PullOut
	_, err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/workspaces/%s/sync?cursor=%d", workspaceID, cursor), nil, &out)
	return &out, err
}

type PushIn struct {
	Rows    map[string][]any    `json:"rows"`
	Deletes map[string][]string `json:"deletes,omitempty"`
}

type PushResult struct {
	Table   string `json:"table"`
	ID      string `json:"id"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

type PushOut struct {
	Results []PushResult `json:"results"`
	Applied int          `json:"applied"`
	Skipped int          `json:"skipped"`
	Failed  int          `json:"failed"`
}

func (c *Client) Push(ctx context.Context, workspaceID string, in PushIn) (*PushOut, error) {
	var out PushOut
	_, err := c.Do(ctx, http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/sync", in, &out)
	return &out, err
}

// PutArtifactContent uploads a body. ok is false (no error) when the server
// has no file store configured (501).
func (c *Client) PutArtifactContent(ctx context.Context, id string, body []byte) (fileID string, ok bool, err error) {
	raw, code, err := c.Raw(ctx, http.MethodPut, "/api/v1/artifacts/"+id+"/content", body, "text/plain; charset=utf-8")
	if err != nil {
		return "", false, err
	}
	if code == http.StatusNotImplemented {
		return "", false, nil
	}
	if code >= 300 {
		return "", false, fmt.Errorf("content upload: %d %s", code, strings.TrimSpace(string(raw)))
	}
	var a struct {
		FileID string `json:"fileId"`
	}
	_ = json.Unmarshal(raw, &a)
	return a.FileID, true, nil
}

// GetArtifactContent downloads a body. ok is false when the server has no
// file store or the artifact has no content.
func (c *Client) GetArtifactContent(ctx context.Context, id string) (body []byte, ok bool, err error) {
	raw, code, err := c.Raw(ctx, http.MethodGet, "/api/v1/artifacts/"+id+"/content", nil, "")
	if err != nil {
		return nil, false, err
	}
	if code == http.StatusNotImplemented || code == http.StatusNotFound {
		return nil, false, nil
	}
	if code >= 300 {
		return nil, false, fmt.Errorf("content download: %d %s", code, strings.TrimSpace(string(raw)))
	}
	return raw, true, nil
}

func urlQuery(s string) string {
	r := strings.NewReplacer("%", "%25", "&", "%26", "+", "%2B", " ", "%20", "#", "%23", "?", "%3F")
	return r.Replace(s)
}
