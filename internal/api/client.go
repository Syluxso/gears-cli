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
	"net"
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
	notModified, _, err = c.DoHeader(ctx, method, path, body, out)
	return notModified, err
}

// DoHeader is Do that also returns the response headers.
func (c *Client) DoHeader(ctx context.Context, method, path string, body any, out any) (notModified bool, hdr http.Header, err error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return false, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Host+path, rdr)
	if err != nil {
		return false, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.auth(req)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return false, nil, fmt.Errorf("%s unreachable: %w", c.Host, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusNotModified {
		return true, resp.Header, nil
	}
	if resp.StatusCode >= 300 {
		var p Problem
		if json.Unmarshal(raw, &p) == nil && p.Status != 0 {
			return false, resp.Header, &p
		}
		return false, resp.Header, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return false, resp.Header, json.Unmarshal(raw, out)
	}
	return false, resp.Header, nil
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

// PresenceConn is one open board stream (GRS-37).
type PresenceConn struct {
	UserID              string    `json:"userId"`
	Name                string    `json:"name,omitempty"`
	Email               string    `json:"email,omitempty"`
	Label               string    `json:"label,omitempty"`
	DeviceID            string    `json:"deviceId,omitempty"`
	DeviceLabel         string    `json:"deviceLabel,omitempty"`
	AuthMethod          string    `json:"authMethod,omitempty"`
	SyncIntervalMinutes int       `json:"syncIntervalMinutes,omitempty"`
	ConnectedAt         time.Time `json:"connectedAt"`
}

// Presence is GET /workspaces/:ws/presence: who holds the board stream now.
type Presence struct {
	Count       int            `json:"count"`
	Connections []PresenceConn `json:"connections"`
}

// Presence asks who holds the workspace's board stream right now. An API
// from before GRS-37 answers 404 (a *Problem).
func (c *Client) Presence(ctx context.Context, ws string) (*Presence, error) {
	var out Presence
	if _, err := c.Do(ctx, http.MethodGet, "/api/v1/workspaces/"+ws+"/presence", nil, &out); err != nil {
		return nil, err
	}
	if out.Connections == nil {
		out.Connections = []PresenceConn{}
	}
	return &out, nil
}

// Person is one person of a workspace (GRS-36): a member, with the agent
// labels their devices use.
type Person struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Email  string   `json:"email"`
	Role   string   `json:"role"`
	Labels []string `json:"agentLabels"`
}

// PeopleOut is GET /workspaces/:ws/people: the people, and which one the
// caller is.
type PeopleOut struct {
	Me     string   `json:"me"`
	People []Person `json:"people"`
}

// People lists a workspace's people.
func (c *Client) People(ctx context.Context, ws string) (*PeopleOut, error) {
	var out PeopleOut
	_, err := c.Do(ctx, http.MethodGet, "/api/v1/workspaces/"+urlQuery(ws)+"/people", nil, &out)
	return &out, err
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
	// Tables echoes a pull's table filter (GRS-51). Asked for tables and
	// got none back: the API predates the filter and sent everything.
	Tables []string `json:"tables,omitempty"`
	// Desk is "visible" from an API with desk privacy (GRS-48): desk items
	// the caller cannot see come down as deletes.
	Desk string `json:"desk,omitempty"`
}

func (c *Client) Pull(ctx context.Context, workspaceID string, cursor int64) (*PullOut, error) {
	var out PullOut
	_, err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/workspaces/%s/sync?cursor=%d", workspaceID, cursor), nil, &out)
	return &out, err
}

// PullTables pulls only the named tables ("cards" is the card tables) after
// a cursor that belongs to that filter (GRS-51). Check out.Tables: an API
// that ignores the filter answers without it.
func (c *Client) PullTables(ctx context.Context, workspaceID string, cursor int64, tables string) (*PullOut, error) {
	var out PullOut
	_, err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/workspaces/%s/sync?cursor=%d&tables=%s", workspaceID, cursor, urlQuery(tables)), nil, &out)
	return &out, err
}

// StreamError is a board stream that did not open: the status and the
// start of the body.
type StreamError struct {
	Status int
	Body   string
}

func (e *StreamError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("board stream: %d %s", e.Status, e.Body)
	}
	return fmt.Sprintf("board stream: %d", e.Status)
}

// Permanent reports an answer retrying will not change: no such route or
// workspace (404, 405, 501, an API from before GRS-48) or a token the API
// refuses (401, 403).
func (e *StreamError) Permanent() bool {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	return false
}

// BoardEvents opens the workspace's live board (GRS-48): Server-Sent Events,
// read as a plain GET with the bearer in Authorization (there is no
// query-string token). The caller reads the body line by line and closes
// it; cancelling ctx ends the stream. No client timeout: the stream is
// long-lived and the server pings every 25s.
func (c *Client) BoardEvents(ctx context.Context, workspaceID string) (io.ReadCloser, error) {
	return c.BoardEventsAs(ctx, workspaceID, StreamHello{})
}

// StreamHello is who holds a board stream, for presence (GRS-37): the
// agent label (X-Gears-Label, over the device's login label) and gears
// run's auto-sync interval in minutes (X-Gears-Sync-Interval; 0 sends none).
type StreamHello struct {
	Label       string
	SyncMinutes int
}

// BoardEventsAs is BoardEvents saying who is holding the stream.
func (c *Client) BoardEventsAs(ctx context.Context, workspaceID string, hello StreamHello) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Host+"/api/v1/workspaces/"+workspaceID+"/board/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.auth(req)
	if l := strings.TrimSpace(hello.Label); l != "" {
		req.Header.Set("X-Gears-Label", l)
	}
	if hello.SyncMinutes > 0 {
		req.Header.Set("X-Gears-Sync-Interval", fmt.Sprint(hello.SyncMinutes))
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s unreachable: %w", c.Host, err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		status := resp.StatusCode
		if status == http.StatusOK {
			status = http.StatusNotImplemented // a 200 that is not a stream: no live board here
		}
		return nil, &StreamError{Status: status, Body: strings.TrimSpace(string(raw))}
	}
	return resp.Body, nil
}

// streamClient has no overall timeout (the board stream is long-lived);
// connecting and the response headers still have bounds.
var streamClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 15 * time.Second,
	IdleConnTimeout:       90 * time.Second,
}}

type PushIn struct {
	Rows    map[string][]any    `json:"rows"`
	Deletes map[string][]string `json:"deletes,omitempty"`
}

type PushResult struct {
	Table    string    `json:"table"`
	ID       string    `json:"id"`
	Applied  bool      `json:"applied"`
	Error    string    `json:"error,omitempty"`
	Assigned *Assigned `json:"assigned,omitempty"`
}

// Assigned is what the API decided for a pushed row (GRS-47): a card's
// final number and key (and the state it used when the sent one was
// unknown), or the id of the existing state a pushed state merged into.
type Assigned struct {
	ID      string `json:"id,omitempty"`
	Number  int    `json:"number,omitempty"`
	Key     string `json:"key,omitempty"`
	StateID string `json:"stateId,omitempty"`
}

// BoardState is one swimlane as the API holds it.
type BoardState struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Position   int       `json:"position"`
	IsDefault  bool      `json:"isDefault"`
	IsTerminal bool      `json:"isTerminal"`
	WIPLimit   int       `json:"wipLimit"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Board is the workspace's one board: its key prefix and states. Cards are
// left out here; sync pulls them as rows.
type Board struct {
	WorkspaceID string       `json:"workspaceId"`
	Prefix      string       `json:"prefix"`
	States      []BoardState `json:"states"`
}

// ErrNoCards means the API answers without card tables: a gears-api from
// before GRS-47, where the board route does not exist.
var ErrNoCards = errors.New("api has no card tables yet; deploy gears-api")

// Board reads the workspace's board. A bare 404 (no route; not a problem
// body) is ErrNoCards.
func (c *Client) Board(ctx context.Context, workspaceID string) (*Board, error) {
	raw, code, err := c.Raw(ctx, http.MethodGet, "/api/v1/workspaces/"+workspaceID+"/board", nil, "")
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
		var p Problem
		if json.Unmarshal(raw, &p) == nil && p.Status != 0 {
			return nil, &p
		}
		return nil, ErrNoCards
	}
	if code >= 300 {
		var p Problem
		if json.Unmarshal(raw, &p) == nil && p.Status != 0 {
			return nil, &p
		}
		return nil, fmt.Errorf("board: %d %s", code, strings.TrimSpace(string(raw)))
	}
	var b Board
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("board: %w", err)
	}
	return &b, nil
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

// StatusError is an answer the API gave with an error status.
type StatusError struct {
	Op   string
	Code int
	Body string
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: %d %s", e.Op, e.Code, e.Body) }

// IsNotFound reports an API answer of 404.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusNotFound
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
		return "", false, &StatusError{Op: "content upload", Code: code, Body: strings.TrimSpace(string(raw))}
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
