package cli

import "github.com/alecthomas/chroma/v2"

var xmlColorLexer = chroma.MustNewLexer(&chroma.Config{Name: "HTTPie XML", DotAll: true}, func() chroma.Rules {
	return chroma.Rules{
		"root": {
			{`[^<&\s]+`, chroma.Text, nil},
			{`[^<&\S]+`, chroma.TextWhitespace, nil},
			{`&\S*?;`, chroma.NameEntity, nil},
			{`<!\[CDATA\[.*?\]\]>`, chroma.CommentPreproc, nil},
			{`<!--.*?-->`, chroma.CommentMultiline, nil},
			{`<\?.*?\?>`, chroma.CommentPreproc, nil},
			{`<![^>]*>`, chroma.CommentPreproc, nil},
			{`<\s*[\w:.-]+`, chroma.NameTag, chroma.Push("tag")},
			{`<\s*/\s*[\w:.-]+\s*>`, chroma.NameTag, nil},
		},
		"tag": {
			{`\s+`, chroma.TextWhitespace, nil},
			{`[\w.:-]+\s*=`, chroma.NameAttribute, chroma.Push("attr")},
			{`/?\s*>`, chroma.NameTag, chroma.Pop(1)},
		},
		"attr": {
			{`\s+`, chroma.TextWhitespace, nil},
			{`".*?"|'.*?'|[^\s>]+`, chroma.LiteralString, chroma.Pop(1)},
		},
	}
})
