package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

func ConfigDir() string {
	if dir := os.Getenv("HTTPIE_CONFIG_DIR"); dir != "" {
		return dir
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "httpie")
	}
	home, _ := os.UserHomeDir()
	legacy := filepath.Join(home, ".httpie")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	root, configured := os.LookupEnv("XDG_CONFIG_HOME")
	if !configured {
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "httpie")
}
func ConfigArgs(args []string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(ConfigDir(), "config.json"))
	if os.IsNotExist(err) {
		return args, nil
	}
	if err != nil {
		return nil, err
	}
	var config any
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("invalid config file: %w", err)
	}
	fields, ok := config.(map[string]any)
	if !ok && config != nil {
		return nil, fmt.Errorf("config must be an object")
	}
	value := fields["default_options"]
	if value == nil || value == false || value == float64(0) || value == "" {
		return args, nil
	}
	if values, ok := value.(map[string]any); ok && len(values) == 0 {
		return args, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("default_options must be an array of strings")
	}
	configuredArgs := make([]string, 0, len(values)+len(args))
	for _, value := range values {
		if value == nil {
			continue
		}
		arg, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("default_options must contain strings")
		}
		configuredArgs = append(configuredArgs, arg)
	}
	return append(configuredArgs, args...), nil
}
func SaveJSON(filename string, data any) error {
	body, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err = os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".httpie-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}

type storedCookie struct {
	Name    string  `json:"name"`
	Value   string  `json:"value"`
	Path    string  `json:"path"`
	Domain  *string `json:"domain"`
	Expires *int64  `json:"expires"`
	Secure  bool    `json:"secure"`
}

func (c *storedCookie) UnmarshalJSON(data []byte) error {
	type cookieJSON storedCookie
	domain := ""
	value := cookieJSON{Path: "/", Domain: &domain}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.Domain != nil {
		*value.Domain = strings.ToLower(*value.Domain)
	}
	*c = storedCookie(value)
	return nil
}

type session struct {
	path     string
	original map[string]json.RawMessage
	headers  []Header
	cookies  []storedCookie
	auth     map[string]any
	exists   bool
}

var sessionName = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

func sessionPath(host, name string) (string, error) {
	if strings.ContainsAny(name, "/\\") {
		return ExpandPath(name), nil
	}
	if !sessionName.MatchString(name) {
		return "", fmt.Errorf("session name contains invalid characters")
	}
	return filepath.Join(ConfigDir(), "sessions", strings.ReplaceAll(host, ":", "_"), name+".json"), nil
}
func loadSession(host, name string) (*session, error) {
	path, err := sessionPath(host, name)
	if err != nil {
		return nil, err
	}
	s := &session{path: path, original: map[string]json.RawMessage{}, headers: []Header{}, cookies: []storedCookie{}, auth: map[string]any{"type": nil, "username": nil, "password": nil}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	s.exists = true
	if err = json.Unmarshal(data, &s.original); err != nil {
		return nil, fmt.Errorf("invalid session: %w", err)
	}
	if raw := s.original["headers"]; len(raw) > 0 {
		if raw[0] == '{' {
			var old map[string]string
			if err = json.Unmarshal(raw, &old); err != nil {
				return nil, err
			}
			parsed, _ := decodeJSON(raw)
			for _, field := range parsed.(object) {
				k := field.key
				s.headers = append(s.headers, Header{k, old[k]})
			}
		} else if err = json.Unmarshal(raw, &s.headers); err != nil {
			return nil, err
		}
	}
	if raw := s.original["cookies"]; len(raw) > 0 {
		if raw[0] == '{' {
			var old map[string]storedCookie
			if err = json.Unmarshal(raw, &old); err != nil {
				return nil, err
			}
			parsed, _ := decodeJSON(raw)
			for _, field := range parsed.(object) {
				k := field.key
				c := old[k]
				c.Name = k
				s.cookies = append(s.cookies, c)
			}
		} else if err = json.Unmarshal(raw, &s.cookies); err != nil {
			return nil, err
		}
	}
	if raw := s.original["auth"]; len(raw) > 0 {
		s.auth = nil
		if err = json.Unmarshal(raw, &s.auth); err != nil {
			return nil, err
		}
	}
	cookies := s.cookies
	s.cookies = []storedCookie{}
	for _, c := range cookies {
		s.setCookie(c)
	}
	return s, nil
}
func (s *session) apply(r *Request, o Options) {
	explicit := map[string]bool{}
	for _, arg := range o.Args {
		it, err := parseItem(arg)
		if err == nil && (it.sep == ":" || it.sep == ";" || it.sep == ":@") {
			explicit[strings.ToLower(it.key)] = true
		}
	}
	seen := map[string]bool{}
	for _, h := range s.headers {
		key := strings.ToLower(h.Name)
		if explicit[key] {
			continue
		}
		if key == "host" {
			r.defaultHost = false
		}
		count := len(r.headers)
		if seen[key] {
			r.headers = append(r.headers, h)
			r.Header.Add(h.Name, h.Value)
		} else {
			r.SetHeader(h.Name, h.Value)
			seen[key] = true
		}
		if len(r.headers) > count {
			r.moveHeaderBefore(h.Name, "Host")
		}
	}
}
func (s *session) credentials() authentication {
	a := authentication{}
	a.kind, _ = s.auth["type"].(string)
	if raw, ok := s.auth["raw_auth"].(string); ok {
		a.raw = raw
		a.username, a.password, _ = splitCredentials(raw)
	} else {
		a.username, _ = s.auth["username"].(string)
		a.password, _ = s.auth["password"].(string)
	}
	return a
}
func (s *session) updateHeaders(r *Request, o Options) error {
	headers := []Header{}
	custom := map[string]bool{}
	for _, arg := range o.Args {
		it, err := parseItem(arg)
		if err == nil && (it.sep == ":" || it.sep == ";" || it.sep == ":@") {
			custom[strings.ToLower(it.key)] = true
		}
	}
	updated := map[string]bool{}
	for _, h := range r.headers {
		key := strings.ToLower(h.Name)
		if !custom[key] && !(key == "accept" && h.Value == "application/json, */*;q=0.5") {
			continue
		}
		if key == "cookie" {
			cookies, err := parseSessionCookies(h.Value)
			if err != nil {
				return err
			}
			for _, c := range cookies {
				s.setCookie(c)
			}
			continue
		}
		if strings.HasPrefix(key, "content-") || strings.HasPrefix(key, "if-") || key == "user-agent" && strings.HasPrefix(h.Value, "HTTPie/") {
			continue
		}
		headers = append(headers, h)
		updated[key] = true
	}
	for _, h := range s.headers {
		if !updated[strings.ToLower(h.Name)] {
			headers = append(headers, h)
		}
	}
	s.headers = headers
	if custom["cookie"] {
		r.deleteHeader("Cookie")
	}
	return nil
}
func (s *session) cookiesFor(u *url.URL) string {
	now := time.Now().Unix()
	s.cookies = slices.DeleteFunc(s.cookies, func(c storedCookie) bool { return c.Expires != nil && *c.Expires <= now })
	cookies := slices.Clone(s.cookies)
	sort.SliceStable(cookies, func(i, j int) bool { return len(cookies[i].Path) > len(cookies[j].Path) })
	var pairs []string
	for _, c := range cookies {
		if c.Secure && u.Scheme != "https" && u.Hostname() != "localhost" && !strings.HasSuffix(u.Hostname(), ".localhost") {
			continue
		}
		if c.Domain != nil && *c.Domain != "" {
			domain := strings.TrimPrefix(*c.Domain, ".")
			host := cookieHost(u)
			if host != domain && !strings.HasSuffix(host, "."+domain) {
				continue
			}
		}
		p := c.Path
		if p == "" {
			p = "/"
		}
		if path := u.EscapedPath(); path != p && !strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			continue
		}
		pairs = append(pairs, c.Name+"="+c.Value)
	}
	return strings.Join(pairs, "; ")
}

// cookieHost mirrors Python's http.cookiejar, which keeps IPv6 brackets and suffixes dotless hosts with ".local".
func cookieHost(u *url.URL) string {
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if !strings.Contains(host, ".") {
		host += ".local"
	}
	return host
}
func (s *session) receiveCookies(cookies []*http.Cookie, u *url.URL) {
	host := cookieHost(u)
	for _, c := range cookies {
		domain := c.Domain
		if domain == "" {
			domain = host
		} else {
			domain = "." + strings.TrimPrefix(strings.ToLower(domain), ".")
		}
		if strings.TrimPrefix(domain, ".") != host && !strings.HasSuffix(host, "."+strings.TrimPrefix(domain, ".")) {
			continue
		}
		p := c.Path
		if p == "" {
			p = "/"
			if slash := strings.LastIndex(u.EscapedPath(), "/"); slash > 0 {
				p = u.EscapedPath()[:slash]
			}
		}
		cookie := storedCookie{Name: c.Name, Value: c.Value, Domain: &domain, Path: p, Secure: c.Secure}
		if !c.Expires.IsZero() {
			v := c.Expires.Unix()
			cookie.Expires = &v
		}
		if c.MaxAge > 0 {
			v := time.Now().Unix() + int64(c.MaxAge)
			cookie.Expires = &v
		}
		if c.MaxAge < 0 || cookie.Expires != nil && *cookie.Expires <= time.Now().Unix() {
			s.cookies = slices.DeleteFunc(s.cookies, func(old storedCookie) bool {
				return old.Name == c.Name && cookieDomain(old) == domain && old.Path == p
			})
			continue
		}
		s.setCookie(cookie)
	}
}
func (s *session) save() error {
	data := map[string]any{}
	for k, v := range s.original {
		data[k] = v
	}
	data["auth"] = s.auth
	data["headers"] = s.headers
	data["cookies"] = s.cookies
	if raw := s.original["headers"]; len(raw) > 0 && raw[0] == '{' {
		old := map[string]string{}
		for _, h := range s.headers {
			old[h.Name] = h.Value
		}
		data["headers"] = old
	}
	if raw := s.original["cookies"]; len(raw) > 0 && raw[0] == '{' {
		old := map[string]map[string]any{}
		for _, c := range s.cookies {
			old[c.Name] = map[string]any{"value": c.Value, "path": c.Path, "domain": c.Domain, "expires": c.Expires, "secure": c.Secure}
		}
		data["cookies"] = old
	}
	meta := map[string]any{}
	if raw := s.original["__meta__"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &meta)
	}
	if _, ok := meta["httpie"]; !ok {
		meta["httpie"] = Version
	}
	meta["about"] = "HTTPie session file"
	meta["help"] = "https://httpie.io/docs#sessions"
	data["__meta__"] = meta
	return SaveJSON(s.path, data)
}

// UpgradeSession rewrites a session file in the format of this version, as
// "httpie cli sessions upgrade" does; bind gives cookies without a domain
// the host's. It reports false for a session already up to date.
func UpgradeSession(host, name string, bind bool) (path string, upgraded bool, err error) {
	s, err := loadSession(host, name)
	if err != nil {
		return "", false, err
	}
	if !s.exists {
		return "", false, fmt.Errorf("session does not exist: %s", s.path)
	}
	meta := map[string]any{}
	_ = json.Unmarshal(s.original["__meta__"], &meta)
	current, ok := meta["httpie"].(string)
	if !ok {
		current = Version
	}
	if !NewerVersion("3.2.0", current) {
		return s.path, false, nil
	}
	if NewerVersion("3.1.0", current) {
		for i := range s.cookies {
			c := &s.cookies[i]
			if c.Domain == nil || *c.Domain == "" {
				if bind {
					domain := strings.Split(host, ":")[0]
					c.Domain = &domain
				} else {
					c.Domain = nil
				}
			}
		}
		s.original["cookies"] = json.RawMessage("[]")
	}
	s.original["headers"] = json.RawMessage("[]")
	meta["httpie"] = Version
	s.original["__meta__"], _ = json.Marshal(meta)
	return s.path, true, s.save()
}
