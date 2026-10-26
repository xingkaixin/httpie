package core

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
	"regexp"
	"runtime"
	"strings"

	"golang.org/x/term"
)

func readPassword(prompt string) (string, error) {
	device := "/dev/tty"
	if runtime.GOOS == "windows" {
		device = "CONIN$"
	}
	terminal, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("unable to prompt for a password: %w", err)
	}
	defer terminal.Close()
	fmt.Fprint(os.Stderr, prompt)
	password, err := term.ReadPassword(int(terminal.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(password), err
}

type authentication struct {
	kind, raw, username, password string
	latin1                        bool
}

func splitCredentials(raw string) (string, string, bool) {
	var username, password strings.Builder
	part := &username
	found := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) {
			if raw[i+1] == ':' {
				i++
				part.WriteByte(':')
				continue
			}
			part.WriteByte(raw[i])
			i++
		} else if raw[i] == ':' && !found {
			part = &password
			found = true
			continue
		}
		part.WriteByte(raw[i])
	}
	if !found {
		return raw, "", false
	}
	return username.String(), password.String(), true
}

func authCredentials(o Options, r *Request) (authentication, error) {
	a := authentication{kind: o.Value("auth-type"), raw: o.Value("auth")}
	provided := o.Has("auth")
	parsed := false
	defer func() { r.URL.User = nil }()
	if !provided && a.kind == "" && r.URL.User != nil {
		a.username = r.URL.User.Username()
		a.password, _ = r.URL.User.Password()
		a.raw = a.username + ":" + a.password
		provided, parsed = true, true
	}
	if a.kind == "" {
		a.kind = "basic"
	}
	if !provided && o.Has("auth-type") && !o.Has("ignore-netrc") && a.kind != "bearer" {
		a.username, a.password, provided = netrcAuth(r.URL.Hostname())
		a.raw = a.username + ":" + a.password
		parsed = provided
	}
	if !provided {
		if o.Has("auth-type") {
			return authentication{}, fmt.Errorf("--auth required")
		}
		return authentication{}, nil
	}
	if a.kind == "bearer" {
		return a, nil
	}
	if !parsed {
		var hasPassword bool
		a.username, a.password, hasPassword = splitCredentials(a.raw)
		if !hasPassword {
			if o.Has("ignore-stdin") {
				return a, fmt.Errorf("unable to prompt for passwords because --ignore-stdin is set")
			}
			var err error
			a.password, err = readPassword(fmt.Sprintf("http: password for %s@%s: ", a.username, r.URL.Host))
			if err != nil {
				return a, err
			}
		}
	}
	if a.username != "" && a.password != "" {
		a.raw = a.username + ":" + a.password
	}
	return a, nil
}
func applyAuth(r *Request, a authentication) error {
	switch a.kind {
	case "basic":
		credentials := []byte(a.username + ":" + a.password)
		if a.latin1 {
			credentials = nil
			for _, c := range a.username + ":" + a.password {
				if c > 255 {
					return fmt.Errorf("netrc credentials cannot be encoded as latin-1")
				}
				credentials = append(credentials, byte(c))
			}
		}
		r.SetHeader("Authorization", "Basic "+base64.StdEncoding.EncodeToString(credentials))
	case "bearer":
		r.SetHeader("Authorization", "Bearer "+a.raw)
	default:
		return nil
	}
	r.moveHeaderBefore("Authorization", "User-Agent")
	return nil
}

var digestField = regexp.MustCompile(`([\w-]+)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^,\s]+))`)
var digestScheme = regexp.MustCompile(`(?i)(?:^|,)\s*digest\s`)

func digestChallenge(values []string) (string, bool) {
	challenges := strings.Join(values, ", ")
	loc := digestScheme.FindStringIndex(challenges)
	if loc == nil {
		return "", false
	}
	return challenges[loc[1]:], true
}

func digestAuthorization(challenge, method, uri string, auth authentication) (string, error) {
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
	username, password := auth.username, auth.password
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
