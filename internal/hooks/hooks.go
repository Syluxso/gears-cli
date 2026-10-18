// Package hooks writes and removes the Gears Stop hook in a Claude Code
// project settings file, <dir>/.claude/settings.json (GRS-93):
//
//	{"hooks": {"Stop": [{"hooks": [{"type": "command",
//	  "command": "gears runbook next --hook", "timeout": 10}]}]}}
//
// The file is merged, never replaced: other keys, other events and other
// Stop hooks stay as they were, in their order. Install is idempotent;
// Uninstall removes only the Gears hook. Output is 2-space JSON.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Marker identifies the Gears Stop hook in a command.
const Marker = "runbook next --hook"

// Timeout is the hook's timeout in seconds (the command answers in ~2s).
const Timeout = 10

// Command is the hook command; agent, when set, is baked in with -a.
func Command(agent string) string {
	if a := strings.TrimSpace(agent); a != "" {
		return "gears -a " + a + " " + Marker
	}
	return "gears " + Marker
}

// MCPAgent is GEARS_AGENT from the env of the gears MCP server (command
// gears, args starting with run) in <dir>/.mcp.json, or "" (GRS-95). Claude
// Code gives that env to the MCP server only, never to a hook command, so
// a hook without it would act as a different label than the agent's tools.
func MCPAgent(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &cfg) != nil {
		return ""
	}
	var found []string
	for _, s := range cfg.MCPServers {
		base := strings.TrimSuffix(strings.ToLower(filepath.Base(s.Command)), ".exe")
		if base != "gears" || len(s.Args) == 0 || s.Args[0] != "run" {
			continue
		}
		if a := strings.TrimSpace(s.Env["GEARS_AGENT"]); a != "" {
			found = append(found, a)
		}
	}
	if len(found) != 1 { // none, or several gears servers: no single answer
		return ""
	}
	return found[0]
}

// Agent is the label the hook command carries: the -a flag, else the gears
// MCP server's GEARS_AGENT in <dir>/.mcp.json, else "" (the label the hook
// resolves when it runs).
func Agent(flag, dir string) string {
	if a := strings.TrimSpace(flag); a != "" {
		return a
	}
	return MCPAgent(dir)
}

// Path is the settings file the hook lives in.
func Path(dir string) string { return filepath.Join(dir, ".claude", "settings.json") }

// Status is what is installed.
type Status struct {
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Command   string `json:"command,omitempty"`
	Changed   bool   `json:"changed,omitempty"`
	Removed   bool   `json:"removed,omitempty"`
}

// ---------------------------------------------------------- ordered JSON

type omap struct {
	keys []string
	vals map[string]any
}

func (m *omap) get(k string) (any, bool) { v, ok := m.vals[k]; return v, ok }

func (m *omap) set(k string, v any) {
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

func (m *omap) del(k string) {
	if _, ok := m.vals[k]; !ok {
		return
	}
	delete(m.vals, k)
	for i, x := range m.keys {
		if x == k {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

func newMap() *omap { return &omap{vals: map[string]any{}} }

// parse reads a JSON value keeping object key order; scalars stay raw.
func parse(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := newMap()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				v, err := parse(dec)
				if err != nil {
					return nil, err
				}
				m.set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return m, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parse(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case json.Number:
		return json.RawMessage(t.String()), nil
	default:
		b, err := marshal(t)
		return json.RawMessage(b), err
	}
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// write prints v as 2-space JSON.
func write(w *bytes.Buffer, v any, indent string) error {
	in := indent + "  "
	switch t := v.(type) {
	case *omap:
		if len(t.keys) == 0 {
			w.WriteString("{}")
			return nil
		}
		w.WriteString("{\n")
		for i, k := range t.keys {
			kb, err := marshal(k)
			if err != nil {
				return err
			}
			w.WriteString(in)
			w.Write(kb)
			w.WriteString(": ")
			if err := write(w, t.vals[k], in); err != nil {
				return err
			}
			if i < len(t.keys)-1 {
				w.WriteString(",")
			}
			w.WriteString("\n")
		}
		w.WriteString(indent + "}")
	case []any:
		if len(t) == 0 {
			w.WriteString("[]")
			return nil
		}
		w.WriteString("[\n")
		for i, x := range t {
			w.WriteString(in)
			if err := write(w, x, in); err != nil {
				return err
			}
			if i < len(t)-1 {
				w.WriteString(",")
			}
			w.WriteString("\n")
		}
		w.WriteString(indent + "]")
	case json.RawMessage:
		w.Write(t)
	default:
		b, err := marshal(t)
		if err != nil {
			return err
		}
		w.Write(b)
	}
	return nil
}

// load reads the settings file; a missing or empty one is an empty object.
func load(path string) (*omap, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newMap(), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(raw)) == 0 {
		return newMap(), true, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := parse(dec)
	if err != nil {
		return nil, true, fmt.Errorf("%s is not valid JSON (%v); left as it is", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, true, fmt.Errorf("%s has more than one JSON value; left as it is", path)
	}
	m, ok := v.(*omap)
	if !ok {
		return nil, true, fmt.Errorf("%s is not a JSON object; left as it is", path)
	}
	return m, true, nil
}

func save(path string, m *omap) error {
	var buf bytes.Buffer
	if err := write(&buf, m, ""); err != nil {
		return err
	}
	buf.WriteString("\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".gears-tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ours reports whether a hook entry is the Gears Stop hook, and its command.
func ours(h any) (string, bool) {
	m, ok := h.(*omap)
	if !ok {
		return "", false
	}
	raw, ok := m.vals["command"].(json.RawMessage)
	if !ok {
		return "", false
	}
	var cmd string
	if json.Unmarshal(raw, &cmd) != nil {
		return "", false
	}
	return cmd, strings.Contains(cmd, Marker)
}

// stopGroups is settings.hooks.Stop, when it is the shape Claude Code reads.
func stopGroups(m *omap) (*omap, []any, error) {
	hv, ok := m.get("hooks")
	if !ok {
		return nil, nil, nil
	}
	hooks, ok := hv.(*omap)
	if !ok {
		return nil, nil, errors.New("settings hooks is not an object; left as it is")
	}
	sv, ok := hooks.get("Stop")
	if !ok {
		return hooks, nil, nil
	}
	stop, ok := sv.([]any)
	if !ok {
		return nil, nil, errors.New("settings hooks.Stop is not an array; left as it is")
	}
	return hooks, stop, nil
}

// find is the Gears hook's command in Stop, if any.
func find(stop []any) (string, bool) {
	for _, g := range stop {
		gm, ok := g.(*omap)
		if !ok {
			continue
		}
		hs, _ := gm.vals["hooks"].([]any)
		for _, h := range hs {
			if cmd, ok := ours(h); ok {
				return cmd, true
			}
		}
	}
	return "", false
}

// Read is what dir's settings hold.
func Read(dir string) (Status, error) {
	st := Status{Path: Path(dir)}
	m, _, err := load(st.Path)
	if err != nil {
		return st, err
	}
	_, stop, err := stopGroups(m)
	if err != nil {
		return st, err
	}
	st.Command, st.Installed = find(stop)
	return st, nil
}

func hookEntry(command string) *omap {
	h := newMap()
	h.set("type", json.RawMessage(`"command"`))
	cb, _ := marshal(command)
	h.set("command", json.RawMessage(cb))
	h.set("timeout", json.RawMessage(fmt.Sprint(Timeout)))
	return h
}

// Install adds the Gears Stop hook to dir's settings (or brings its command
// up to date). Changed is false when it was already there as asked.
func Install(dir, command string) (Status, error) {
	st := Status{Path: Path(dir), Command: command, Installed: true}
	m, _, err := load(st.Path)
	if err != nil {
		return st, err
	}
	hooks, stop, err := stopGroups(m)
	if err != nil {
		return st, err
	}
	if hooks == nil {
		hooks = newMap()
		m.set("hooks", hooks)
	}
	found := false
	for _, g := range stop {
		gm, ok := g.(*omap)
		if !ok {
			continue
		}
		hs, _ := gm.vals["hooks"].([]any)
		for i, h := range hs {
			cmd, ok := ours(h)
			if !ok {
				continue
			}
			if found { // a duplicate from a hand edit: one is enough
				continue
			}
			found = true
			if cmd != command {
				hs[i] = hookEntry(command)
				st.Changed = true
			}
		}
	}
	if !found {
		g := newMap()
		g.set("hooks", []any{hookEntry(command)})
		stop = append(stop, g)
		hooks.set("Stop", stop)
		st.Changed = true
	}
	if !st.Changed {
		return st, nil
	}
	return st, save(st.Path, m)
}

// Uninstall removes the Gears Stop hook from dir's settings: a group left
// empty goes, Stop and hooks too when nothing else is in them. Everything
// else stays. A settings file holding nothing else is removed.
func Uninstall(dir string) (Status, error) {
	st := Status{Path: Path(dir)}
	m, exists, err := load(st.Path)
	if err != nil || !exists {
		return st, err
	}
	hooks, stop, err := stopGroups(m)
	if err != nil || hooks == nil {
		return st, err
	}
	var keep []any
	for _, g := range stop {
		gm, ok := g.(*omap)
		if !ok {
			keep = append(keep, g)
			continue
		}
		hs, isArr := gm.vals["hooks"].([]any)
		if !isArr {
			keep = append(keep, g)
			continue
		}
		var left []any
		for _, h := range hs {
			if _, ok := ours(h); ok {
				st.Removed = true
				continue
			}
			left = append(left, h)
		}
		if len(left) == len(hs) {
			keep = append(keep, g)
			continue
		}
		if len(left) == 0 {
			continue
		}
		gm.vals["hooks"] = left
		keep = append(keep, g)
	}
	if !st.Removed {
		return st, nil
	}
	st.Changed = true
	if len(keep) == 0 {
		hooks.del("Stop")
	} else {
		hooks.set("Stop", keep)
	}
	if len(hooks.keys) == 0 {
		m.del("hooks")
	}
	if len(m.keys) == 0 {
		return st, os.Remove(st.Path)
	}
	return st, save(st.Path, m)
}
