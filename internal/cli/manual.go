package cli

import (
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

func printManual(program string, stdin io.Reader, stdout, stderr io.Writer, terminal bool) error {
	if os.Getenv("HTTPIE_NO_MAN_PAGES") == "" && exec.Command("man", "1", program).Run() == nil {
		command := exec.Command("man", "1", program)
		command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
		_ = command.Run()
		return nil
	}
	width := helpWidth(stdout)
	text := wrapHelp(formatHelp(width), width)
	if terminal {
		pager := os.Getenv("MANPAGER")
		if pager == "" {
			pager = os.Getenv("PAGER")
		}
		if pager == "" && os.Getenv("TERM") != "dumb" && os.Getenv("TERM") != "emacs" {
			for _, name := range []string{"less", "more"} {
				if _, err := exec.LookPath(name); err == nil {
					pager = name
					break
				}
			}
		}
		if pager != "" {
			command := exec.Command("sh", "-c", pager)
			command.Stdin, command.Stdout, command.Stderr = strings.NewReader(text), stdout, stderr
			_ = command.Run()
			return nil
		}
	}
	_, err := io.WriteString(stdout, text)
	return err
}

func helpWidth(stdout io.Writer) int {
	width, _ := strconv.Atoi(os.Getenv("COLUMNS"))
	if width <= 0 {
		if file, ok := stdout.(*os.File); ok {
			width, _, _ = term.GetSize(int(file.Fd()))
		}
	}
	if width <= 0 {
		width = 80
	}
	return width
}

func formatHelp(width int) string {
	width -= 2
	parts := []string{"http", "[METHOD]", "URL", "[REQUEST_ITEM ...]"}
	usage, line, indent := "usage:\n    ", "", 16
	used := 11
	if 15 > float64(width)*0.75 {
		usage += "http\n"
		parts = parts[1:]
		indent = 11
		line = strings.Repeat(" ", indent)
		used = indent
	}
	for i, part := range parts {
		if i > 0 && used+1+len(part) > width {
			usage += line + "\n"
			line = strings.Repeat(" ", indent)
			used = indent
		} else if i > 0 {
			line += " "
			used++
		}
		line += part
		used += len(part)
	}
	_, text, _ := strings.Cut(help, "\n\n")
	if width < 26 {
		lines := strings.Split(text, "\n")
		indent := min(6, max(width-20, 4))
		for i, line := range lines {
			if strings.HasPrefix(line, "      ") {
				lines[i] = line[6-indent:]
			}
		}
		text = strings.Join(lines, "\n")
	}
	return usage + line + "\n\n" + text
}

var helpWord = regexp.MustCompile(`\s*\S+\s*`)

func wrapHelp(text string, width int) string {
	var result strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(text, "[filename]", ""), "\n") {
		start, used := 0, 0
		write := func(end int) {
			text := line[start:end]
			if utf8.RuneCountInString(text) > width {
				text = string([]rune(text)[:width])
			}
			result.WriteString(text)
			result.WriteByte('\n')
			start = end
		}
		for _, word := range helpWord.FindAllStringIndex(line, -1) {
			length := utf8.RuneCountInString(strings.TrimRight(line[word[0]:word[1]], " \t"))
			if used+length <= width {
				used += utf8.RuneCountInString(line[word[0]:word[1]])
				continue
			}
			if word[0] > start {
				write(word[0])
			}
			for utf8.RuneCountInString(line[start:word[1]]) > width && length > width {
				write(start + len(string([]rune(line[start:word[1]])[:width])))
				length -= width
			}
			used = utf8.RuneCountInString(line[start:word[1]])
		}
		write(len(line))
	}
	return result.String()
}
