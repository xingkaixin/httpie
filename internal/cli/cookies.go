package cli

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func cookieDomain(c storedCookie) string {
	if c.Domain == nil {
		return ""
	}
	return *c.Domain
}
func (s *session) setCookie(cookie storedCookie) {
	domain := cookieDomain(cookie)
	insert, domainEnd := len(s.cookies), -1
	for i, old := range s.cookies {
		if cookieDomain(old) != domain {
			continue
		}
		domainEnd = i + 1
		if old.Path == cookie.Path {
			if old.Name == cookie.Name {
				s.cookies[i] = cookie
				return
			}
			insert = i + 1
		}
	}
	if insert == len(s.cookies) && domainEnd >= 0 {
		insert = domainEnd
	}
	s.cookies = slices.Insert(s.cookies, insert, cookie)
}

var sessionCookiePattern = regexp.MustCompile(`^\s*([\w!#$%&'*+\-.^` + "`" + `|~:<>@,/?{}\[\]()]+?)(?:\s*=\s*("(?:[^"\\]|\\.)*"|\w{3},\s[\w\d\s-]{9,11}\s[\d:]{8}\sGMT|[\w!#$%&'*+\-.^` + "`" + `|~:<>@,/?{}\[\]()=]*))?\s*(?:\s+|;|$)`)

func unquoteCookie(value string) string {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return value
	}
	value = value[1 : len(value)-1]
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) {
			i++
			if i+2 < len(value) && value[i] >= '0' && value[i] <= '3' && value[i+1] >= '0' && value[i+1] <= '7' && value[i+2] >= '0' && value[i+2] <= '7' {
				n, _ := strconv.ParseInt(value[i:i+3], 8, 16)
				out.WriteRune(rune(n))
				i += 2
				continue
			}
		}
		out.WriteByte(value[i])
	}
	return out.String()
}
func parseSessionCookies(text string) ([]storedCookie, error) {
	var cookies []storedCookie
	var attrs []map[string]string
	current := -1
	for text != "" {
		match := sessionCookiePattern.FindStringSubmatchIndex(text)
		if match == nil {
			break
		}
		name := text[match[2]:match[3]]
		value, supplied := "", match[4] >= 0
		if supplied {
			value = unquoteCookie(text[match[4]:match[5]])
		}
		text = text[match[1]:]
		if strings.HasPrefix(name, "$") {
			continue
		}
		key := strings.ToLower(name)
		switch key {
		case "expires", "path", "comment", "domain", "max-age", "secure", "httponly", "version", "samesite":
			if current < 0 || !supplied && key != "secure" && key != "httponly" {
				return nil, nil
			}
			if !supplied {
				value = "true"
			}
			attrs[current][key] = value
		default:
			if !supplied {
				return nil, nil
			}
			current = slices.IndexFunc(cookies, func(c storedCookie) bool { return c.Name == name })
			if current < 0 {
				current = len(cookies)
				cookies = append(cookies, storedCookie{Name: name})
				attrs = append(attrs, map[string]string{})
			}
			cookies[current].Value = value
		}
	}
	for i := range cookies {
		c, attr := &cookies[i], attrs[i]
		domain := strings.ToLower(attr["domain"])
		c.Domain = &domain
		c.Path = attr["path"]
		if c.Path == "" {
			c.Path = "/"
		}
		c.Secure = attr["secure"] != ""
		if age := attr["max-age"]; age != "" {
			n, err := strconv.ParseInt(age, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid cookie max-age: %s", age)
			}
			expires := time.Now().Unix() + n
			c.Expires = &expires
		} else if value := attr["expires"]; value != "" {
			expires, err := http.ParseTime(value)
			if err != nil {
				return nil, err
			}
			n := expires.Unix()
			c.Expires = &n
		}
	}
	return cookies, nil
}
