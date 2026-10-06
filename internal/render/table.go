package render

import (
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// table aligns columns by their *visible* width. text/tabwriter counts ANSI
// color codes as characters, which misaligns any colored cell.
type table struct {
	indent string
	rows   [][]string
	header bool
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func visibleLen(s string) int { return utf8.RuneCountInString(ansi.ReplaceAllString(s, "")) }

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

func (t *table) write(w io.Writer, p pen) {
	if len(t.rows) == 0 {
		return
	}
	cols := 0
	for _, r := range t.rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	width := make([]int, cols)
	for _, r := range t.rows {
		for i, c := range r {
			if n := visibleLen(c); n > width[i] {
				width[i] = n
			}
		}
	}
	for ri, r := range t.rows {
		var b strings.Builder
		b.WriteString(t.indent)
		for i, c := range r {
			if t.header && ri == 0 {
				c = p.c(grey, c)
			}
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", width[i]-visibleLen(c)+2))
			}
		}
		io.WriteString(w, strings.TrimRight(b.String(), " ")+"\n")
	}
}

// clip shortens s to n visible characters with an ellipsis.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// joinClip joins names until the cell would exceed n characters, then "+N more".
func joinClip(names []string, n int) string {
	var out []string
	used := 0
	for i, s := range names {
		add := len(s)
		if i > 0 {
			add += 2
		}
		if used+add > n && len(out) > 0 {
			out = append(out, "+"+strconv.Itoa(len(names)-i)+" more")
			break
		}
		out = append(out, s)
		used += add
	}
	return strings.Join(out, ", ")
}
