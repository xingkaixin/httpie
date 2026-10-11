package cli

import (
	"strings"
	"unicode/utf8"

	"github.com/wlynxg/chardet"
	"github.com/wlynxg/chardet/lookup"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func textEncoding(name string) encoding.Encoding {
	name = strings.ReplaceAll(strings.ToLower(name), "_", "-")
	if name == "utf-8-sig" {
		return unicode.UTF8BOM
	}
	if strings.HasPrefix(name, "cp125") {
		name = "windows-" + name[2:]
	}
	enc, _ := lookup.LookupEncoding(name)
	if enc == nil {
		enc, _ = charset.Lookup(name)
	}
	return enc
}

func decodeText(data []byte, name string) []byte {
	if name == "" && !utf8.Valid(data) && len(data) > 32 {
		name = chardet.Detect(data).Charset
	}
	if enc := textEncoding(name); enc != nil {
		decoded, _, err := transform.Bytes(enc.NewDecoder(), data)
		if err == nil {
			return decoded
		}
	}
	return data
}
