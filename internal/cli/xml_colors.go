package cli

import "github.com/alecthomas/chroma/v2"

var xmlColorLexer = chroma.MustNewLexer(&chroma.Config{Name: "HTTPie XML", DotAll: true}, func() chroma.Rules {
	return chroma.Rules{
		"root": {
			{Pattern: `[^<&\s]+`, Type: chroma.Text},
			{Pattern: `[^<&\S]+`, Type: chroma.TextWhitespace},
			{Pattern: `&\S*?;`, Type: chroma.NameEntity},
			{Pattern: `<!\[CDATA\[.*?\]\]>`, Type: chroma.CommentPreproc},
			{Pattern: `<!--.*?-->`, Type: chroma.CommentMultiline},
			{Pattern: `<\?.*?\?>`, Type: chroma.CommentPreproc},
			{Pattern: `<![^>]*>`, Type: chroma.CommentPreproc},
			{Pattern: `<\s*[\w:.-]+`, Type: chroma.NameTag, Mutator: chroma.Push("tag")},
			{Pattern: `<\s*/\s*[\w:.-]+\s*>`, Type: chroma.NameTag},
		},
		"tag": {
			{Pattern: `\s+`, Type: chroma.TextWhitespace},
			{Pattern: `[\w.:-]+\s*=`, Type: chroma.NameAttribute, Mutator: chroma.Push("attr")},
			{Pattern: `/?\s*>`, Type: chroma.NameTag, Mutator: chroma.Pop(1)},
		},
		"attr": {
			{Pattern: `\s+`, Type: chroma.TextWhitespace},
			{Pattern: `".*?"|'.*?'|[^\s>]+`, Type: chroma.LiteralString, Mutator: chroma.Pop(1)},
		},
	}
})
