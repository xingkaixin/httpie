package cli

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/term"
)

func readPassword(prompt string) (string, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("unable to prompt for a password: %w", err)
	}
	defer terminal.Close()
	fmt.Fprint(terminal, prompt)
	password, err := term.ReadPassword(int(terminal.Fd()))
	fmt.Fprintln(terminal)
	return string(password), err
}
func authCredentials(o options, r *request) (string, string, error) {
	kind := o.value("auth-type")
	if kind == "" {
		kind = "basic"
	}
	raw := o.value("auth")
	provided := o.has("auth")
	if !provided && !o.has("auth-type") && r.URL.User != nil {
		pass, _ := r.URL.User.Password()
		raw = r.URL.User.Username() + ":" + pass
		provided = true
	}
	if !provided && !o.has("ignore-netrc") && kind != "bearer" {
		raw, provided = netrcAuth(r.URL.Hostname())
	}
	r.URL.User = nil
	if !provided {
		if o.has("auth-type") {
			return "", "", fmt.Errorf("--auth required")
		}
		return "", "", nil
	}
	if kind == "bearer" {
		return kind, raw, nil
	}
	username, password, ok := strings.Cut(raw, ":")
	if !ok {
		if o.has("ignore-stdin") {
			return "", "", fmt.Errorf("unable to prompt for passwords because --ignore-stdin is set")
		}
		var err error
		password, err = readPassword(fmt.Sprintf("http: password for %s@%s: ", username, r.URL.Host))
		if err != nil {
			return "", "", err
		}
	}
	return kind, username + ":" + password, nil
}
func applyAuth(r *request, kind, raw string) {
	switch kind {
	case "basic":
		r.setHeader("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(raw)))
	case "bearer":
		r.setHeader("Authorization", "Bearer "+raw)
	}
	if kind != "basic" && kind != "bearer" {
		return
	}
	auth := r.headers[len(r.headers)-1]
	for i, h := range r.headers {
		if strings.EqualFold(h.Name, "User-Agent") {
			copy(r.headers[i+1:], r.headers[i:len(r.headers)-1])
			r.headers[i] = auth
			break
		}
	}
}

var netrcToken = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'[^']*'|[^\s]+`)

func netrcAuth(host string) (string, bool) {
	filename := os.Getenv("NETRC")
	if filename == "" {
		home, _ := os.UserHomeDir()
		filename = filepath.Join(home, ".netrc")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", false
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		lines = append(lines, line)
	}
	tokens := netrcToken.FindAllString(strings.Join(lines, "\n"), -1)
	machine, user, pass := "", "", ""
	fallback := ""
	save := func() (string, bool) {
		if machine == host {
			return user + ":" + pass, true
		}
		if machine == "default" {
			fallback = user + ":" + pass
		}
		return "", false
	}
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token == "machine" || token == "default" {
			if value, ok := save(); ok {
				return value, true
			}
			user = ""
			pass = ""
			machine = "default"
			if token == "machine" && i+1 < len(tokens) {
				i++
				machine = tokens[i]
			}
			continue
		}
		if i+1 >= len(tokens) {
			break
		}
		switch token {
		case "login", "account":
			i++
			user = strings.Trim(tokens[i], "\"'")
		case "password":
			i++
			pass = strings.Trim(tokens[i], "\"'")
		}
	}
	if value, ok := save(); ok {
		return value, true
	}
	return fallback, fallback != ""
}

var digestField = regexp.MustCompile(`([\w-]+)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^,\s]+))`)

func digestAuthorization(challenge, method, uri, credentials string) (string, error) {
	fields := map[string]string{}
	for _, m := range digestField.FindAllStringSubmatch(challenge, -1) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		fields[strings.ToLower(m[1])] = v
	}
	algorithm := strings.ToUpper(fields["algorithm"])
	if algorithm == "" {
		algorithm = "MD5"
	}
	hash := func(s string) string {
		switch strings.TrimSuffix(algorithm, "-SESS") {
		case "MD5":
			h := md5.Sum([]byte(s))
			return hex.EncodeToString(h[:])
		case "SHA", "SHA-1":
			h := sha1.Sum([]byte(s))
			return hex.EncodeToString(h[:])
		case "SHA-256":
			h := sha256.Sum256([]byte(s))
			return hex.EncodeToString(h[:])
		case "SHA-512":
			h := sha512.Sum512([]byte(s))
			return hex.EncodeToString(h[:])
		}
		return ""
	}
	username, password, _ := strings.Cut(credentials, ":")
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	cnonce := hex.EncodeToString(nonce)
	a1 := hash(username + ":" + fields["realm"] + ":" + password)
	if a1 == "" {
		return "", fmt.Errorf("unsupported Digest algorithm: %s", algorithm)
	}
	if strings.HasSuffix(algorithm, "-SESS") {
		a1 = hash(a1 + ":" + fields["nonce"] + ":" + cnonce)
	}
	a2 := hash(method + ":" + uri)
	qop := ""
	if fields["qop"] != "" {
		for _, v := range strings.Split(fields["qop"], ",") {
			if strings.TrimSpace(v) == "auth" {
				qop = "auth"
			}
		}
		if qop == "" {
			return "", fmt.Errorf("unsupported Digest qop: %s", fields["qop"])
		}
	}
	response := hash(a1 + ":" + fields["nonce"] + ":" + a2)
	if qop != "" {
		response = hash(a1 + ":" + fields["nonce"] + ":00000001:" + cnonce + ":" + qop + ":" + a2)
	}
	escape := func(s string) string { return strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s) }
	result := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm="%s"`, escape(username), escape(fields["realm"]), escape(fields["nonce"]), escape(uri), response, algorithm)
	if fields["opaque"] != "" {
		result += fmt.Sprintf(`, opaque="%s"`, escape(fields["opaque"]))
	}
	if qop != "" {
		result += fmt.Sprintf(`, qop="auth", nc=00000001, cnonce="%s"`, cnonce)
	}
	return result, nil
}
