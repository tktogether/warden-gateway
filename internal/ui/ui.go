package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type UI struct {
	Out   io.Writer
	In    io.Reader
	Color bool
}

func New() *UI {
	return &UI{Out: os.Stdout, In: os.Stdin, Color: useColor()}
}

func (u *UI) Title(title string) {
	u.line()
	u.line("  " + u.c("1;36", title))
	u.line("  " + strings.Repeat("-", len([]rune(title))+2))
}

func (u *UI) Section(title string) {
	u.line()
	u.line(u.c("1;36", "◆ "+title))
}

func (u *UI) Info(format string, args ...interface{}) {
	u.line("  " + fmt.Sprintf(format, args...))
}

func (u *UI) KV(key, value string) {
	u.line(fmt.Sprintf("  %s %s", u.c("2", pad(key, 22)), value))
}

func (u *UI) OK(format string, args ...interface{}) {
	u.line("  " + u.c("32", "✓ ") + fmt.Sprintf(format, args...))
}

func (u *UI) Warn(format string, args ...interface{}) {
	u.line("  " + u.c("33", "! ") + fmt.Sprintf(format, args...))
}

func (u *UI) Error(format string, args ...interface{}) {
	u.line("  " + u.c("31", "✗ ") + fmt.Sprintf(format, args...))
}

func (u *UI) Step(format string, args ...interface{}) {
	u.line("  " + u.c("36", "→ ") + fmt.Sprintf(format, args...))
}

func (u *UI) Ask(label, def string) (string, error) {
	suffix := ""
	if def != "" {
		suffix = " [" + def + "]"
	}
	u.line("  " + u.c("1", label+suffix+": "))
	fmt.Fprint(u.Out, "  ")
	r := bufio.NewReader(u.In)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

func (u *UI) AskYes(label string, def bool) (bool, error) {
	defText := "y/n"
	if def {
		defText = "Y/n"
	} else {
		defText = "y/N"
	}
	answer, err := u.Ask(label+" ("+defText+")", "")
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer == "" {
		return def, nil
	}
	return answer == "y" || answer == "yes" || answer == "是", nil
}

func (u *UI) Choose(label string, options []string, def int) (int, error) {
	u.line("")
	u.line("  " + u.c("1", label))
	for i, opt := range options {
		u.line(fmt.Sprintf("  %s %s", u.c("36", "["+strconv.Itoa(i+1)+"]"), opt))
	}
	answer, err := u.Ask("请选择", strconv.Itoa(def))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || n < 1 || n > len(options) {
		return 0, fmt.Errorf("无效选项: %s", answer)
	}
	return n - 1, nil
}

func (u *UI) Table(headers []string, rows [][]string) {
	if len(rows) == 0 {
		u.line("  （无数据）")
		return
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len([]rune(h))
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len([]rune(cell)) > widths[i] {
				widths[i] = len([]rune(cell))
			}
		}
	}
	var sb strings.Builder
	for i, h := range headers {
		sb.WriteString(pad(h, widths[i]))
		sb.WriteString("  ")
	}
	u.line("  " + u.c("2", strings.TrimRight(sb.String(), " ")))
	for _, row := range rows {
		var sb strings.Builder
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			sb.WriteString(pad(cell, widths[i]))
			sb.WriteString("  ")
		}
		u.line("  " + strings.TrimRight(sb.String(), " "))
	}
}

func (u *UI) line(parts ...string) {
	fmt.Fprintln(u.Out, strings.Join(parts, ""))
}

func (u *UI) c(code, s string) string {
	if !u.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func pad(s string, width int) string {
	n := len([]rune(s))
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

func useColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
