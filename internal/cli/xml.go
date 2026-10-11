package cli

import (
	"bytes"
	"encoding/xml"
	"io"
	"regexp"
	"strings"
)

type xmlNode struct {
	kind, text string
	attributes []xml.Attr
	children   []*xmlNode
}

var xmlDoctype = regexp.MustCompile(`(?s)^DOCTYPE\s+(\S+)\s+(PUBLIC|SYSTEM)\s+["'](.*?)["'](?:\s+["'](.*?)["'])?\s*$`)

func formatXML(data []byte, indent int) []byte {
	if bytes.Contains(bytes.ToUpper(data), []byte("<!ENTITY")) {
		return data
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	root := &xmlNode{kind: "document"}
	stack := []*xmlNode{root}
	qualified := func(name xml.Name) string {
		if name.Space != "" {
			return name.Space + ":" + name.Local
		}
		return name.Local
	}
	for {
		offset := decoder.InputOffset()
		token, err := decoder.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return data
		}
		parent := stack[len(stack)-1]
		var node *xmlNode
		switch value := token.(type) {
		case xml.StartElement:
			node = &xmlNode{kind: "element", text: qualified(value.Name), attributes: value.Attr}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) < 2 || stack[len(stack)-1].text != qualified(value.Name) {
				return data
			}
			stack = stack[:len(stack)-1]
			continue
		case xml.CharData:
			kind := "text"
			if bytes.HasPrefix(data[offset:decoder.InputOffset()], []byte("<![CDATA[")) {
				kind = "cdata"
			}
			node = &xmlNode{kind: kind, text: string(value)}
		case xml.Comment:
			node = &xmlNode{kind: "comment", text: string(value)}
		case xml.ProcInst:
			node = &xmlNode{kind: "instruction", text: string(data[offset:decoder.InputOffset()])}
		case xml.Directive:
			node = &xmlNode{kind: "directive", text: string(value)}
		}
		if node != nil {
			parent.children = append(parent.children, node)
		}
	}
	if len(stack) != 1 {
		return data
	}
	var out strings.Builder
	escape := func(text string, attribute bool) string {
		text = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
		if attribute {
			text = strings.NewReplacer(`"`, "&quot;", "\t", "&#9;", "\n", "&#10;", "\r", "&#13;").Replace(text)
		}
		return text
	}
	var write func(*xmlNode, string, string)
	write = func(node *xmlNode, prefix, newline string) {
		switch node.kind {
		case "document":
			for _, child := range node.children {
				write(child, "", "\n")
			}
		case "text":
			out.WriteString(escape(prefix+node.text+newline, false))
		case "cdata":
			out.WriteString("<![CDATA[" + node.text + "]]>")
		case "comment":
			out.WriteString(prefix + "<!--" + node.text + "-->" + newline)
		case "instruction":
			out.WriteString(prefix + node.text + newline)
		case "directive":
			if match := xmlDoctype.FindStringSubmatch(node.text); match != nil {
				out.WriteString("<!DOCTYPE " + match[1] + "\n" + strings.Repeat(" ", indent) + match[2] + " '" + match[3] + "'")
				if match[4] != "" {
					out.WriteString("\n" + strings.Repeat(" ", indent) + "'" + match[4] + "'")
				}
				out.WriteString(">" + newline)
			} else {
				out.WriteString("<!" + node.text + ">" + newline)
			}
		case "element":
			out.WriteString(prefix + "<" + node.text)
			for _, a := range node.attributes {
				out.WriteString(" " + qualified(a.Name) + `="` + escape(a.Value, true) + `"`)
			}
			if len(node.children) == 0 {
				out.WriteString("/>" + newline)
				return
			}
			out.WriteByte('>')
			if len(node.children) == 1 && (node.children[0].kind == "text" || node.children[0].kind == "cdata") {
				write(node.children[0], "", "")
			} else {
				out.WriteString(newline)
				for _, child := range node.children {
					write(child, prefix+strings.Repeat(" ", indent), newline)
				}
				out.WriteString(prefix)
			}
			out.WriteString("</" + node.text + ">" + newline)
		}
	}
	write(root, "", "\n")
	var lines []string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
