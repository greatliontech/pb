package format

import (
	"bytes"
	"strconv"
	"strings"
)

// Unified is the unified diff from old to new as `pb format --diff`
// prints it (REQ-format-verb): headed `--- path` and `+++ path`, hunks
// over the lines that differ with three lines of context, a last line
// without a newline noted after it; empty where the two are equal.
func Unified(path string, old, new []byte) []byte {
	a, b := lines(old), lines(new)
	ops := edits(a, b)
	if len(ops) == 0 {
		return nil
	}
	const context = 3
	var out bytes.Buffer
	out.WriteString("--- " + path + "\n+++ " + path + "\n")
	// Hunks: runs of edits, two joined where no more than 2*context
	// equal lines separate them.
	type span struct{ from, to int } // over ops, [from, to)
	var hunks []span
	for k := 0; k < len(ops); k++ {
		if ops[k].kind == keep {
			continue
		}
		from := max(k-context, 0)
		to := k + 1
		for to < len(ops) {
			next := to
			for next < len(ops) && ops[next].kind == keep {
				next++
			}
			if next == len(ops) || next-to > 2*context {
				break
			}
			to = next + 1
		}
		to = min(to+context, len(ops))
		hunks = append(hunks, span{from, to})
		k = to - 1
	}
	for _, h := range hunks {
		oldStart, oldCount, newStart, newCount := 0, 0, 0, 0
		for k := 0; k < h.from; k++ {
			if ops[k].kind != insert {
				oldStart++
			}
			if ops[k].kind != remove {
				newStart++
			}
		}
		for _, op := range ops[h.from:h.to] {
			if op.kind != insert {
				oldCount++
			}
			if op.kind != remove {
				newCount++
			}
		}
		out.WriteString("@@ -" + hunkRange(oldStart, oldCount) + " +" + hunkRange(newStart, newCount) + " @@\n")
		for _, op := range ops[h.from:h.to] {
			switch op.kind {
			case keep:
				out.WriteString(" " + op.line)
			case remove:
				out.WriteString("-" + op.line)
			case insert:
				out.WriteString("+" + op.line)
			}
			out.WriteString("\n")
			if !op.newline {
				out.WriteString("\\ No newline at end of file\n")
			}
		}
	}
	return out.Bytes()
}

// hunkRange spells a hunk's side: `start,count`, the start one-based,
// or `start` alone for one line, and `0,0` for none.
func hunkRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start + 1)
	}
	if count == 0 {
		return strconv.Itoa(start) + ",0"
	}
	return strconv.Itoa(start+1) + "," + strconv.Itoa(count)
}

// line is one line of a side, and whether a newline ended it.
type line struct {
	text    string
	newline bool
}

// lines splits a file into its lines, the last one marked where no
// newline ends it; an empty file has no lines.
func lines(b []byte) []line {
	if len(b) == 0 {
		return nil
	}
	parts := strings.SplitAfter(string(b), "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	out := make([]line, len(parts))
	for k, p := range parts {
		out[k] = line{text: strings.TrimSuffix(p, "\n"), newline: strings.HasSuffix(p, "\n")}
	}
	return out
}

type editKind int

const (
	keep editKind = iota
	remove
	insert
)

// edit is one line of the diff: kept, removed from old, or inserted
// from new.
type edit struct {
	kind    editKind
	line    string
	newline bool
}

// edits is a shortest edit script from a to b over lines (equal text
// and newline), removals before insertions at each divergence; nil
// where the two are equal. The script is Myers' — time proportional
// to the lines times the edit distance, space to the lines alone —
// found by divide and conquer on the middle snake of each
// subproblem.
func edits(a, b []line) []edit {
	if len(a) == len(b) {
		same := true
		for k := range a {
			if a[k] != b[k] {
				same = false
				break
			}
		}
		if same {
			return nil
		}
	}
	d := &differ{a: a, b: b}
	d.solve(0, len(a), 0, len(b))
	return grouped(d.out)
}

// grouped orders each run of edits between kept lines as diff -u
// prints it: the removals, then the insertions.
func grouped(ops []edit) []edit {
	out := make([]edit, 0, len(ops))
	for k := 0; k < len(ops); {
		if ops[k].kind == keep {
			out = append(out, ops[k])
			k++
			continue
		}
		end := k
		for end < len(ops) && ops[end].kind != keep {
			end++
		}
		for _, op := range ops[k:end] {
			if op.kind == remove {
				out = append(out, op)
			}
		}
		for _, op := range ops[k:end] {
			if op.kind == insert {
				out = append(out, op)
			}
		}
		k = end
	}
	return out
}

// differ writes the edit script of a[lo:hi] against b[lo:hi] by
// recursion on middle snakes.
type differ struct {
	a, b   []line
	out    []edit
	vf, vb []int // the furthest-reaching forward and backward paths, by diagonal
}

func (d *differ) keepLines(from, to int) {
	for k := from; k < to; k++ {
		d.out = append(d.out, edit{keep, d.a[k].text, d.a[k].newline})
	}
}

func (d *differ) removeLines(from, to int) {
	for k := from; k < to; k++ {
		d.out = append(d.out, edit{remove, d.a[k].text, d.a[k].newline})
	}
}

func (d *differ) insertLines(from, to int) {
	for k := from; k < to; k++ {
		d.out = append(d.out, edit{insert, d.b[k].text, d.b[k].newline})
	}
}

// solve emits the script for a[alo:ahi] against b[blo:bhi].
func (d *differ) solve(alo, ahi, blo, bhi int) {
	// The common prefix and suffix are kept lines around the problem.
	for alo < ahi && blo < bhi && d.a[alo] == d.b[blo] {
		d.out = append(d.out, edit{keep, d.a[alo].text, d.a[alo].newline})
		alo++
		blo++
	}
	suffix := 0
	for alo < ahi-suffix && blo < bhi-suffix && d.a[ahi-1-suffix] == d.b[bhi-1-suffix] {
		suffix++
	}
	ahi, bhi = ahi-suffix, bhi-suffix
	switch {
	case alo == ahi:
		d.insertLines(blo, bhi)
	case blo == bhi:
		d.removeLines(alo, ahi)
	default:
		x, y, u, v := d.middleSnake(alo, ahi, blo, bhi)
		d.solve(alo, x, blo, y)
		d.keepLines(x, u)
		d.solve(u, ahi, v, bhi)
	}
	d.keepLines(ahi, ahi+suffix)
}

// middleSnake finds a snake (x, y) to (u, v) on a shortest edit path
// of a[alo:ahi] against b[blo:bhi], the two halves of the problem on
// either side of it, by furthest-reaching paths run from both ends
// until they meet (Myers 1986, the linear-space refinement).
func (d *differ) middleSnake(alo, ahi, blo, bhi int) (x, y, u, v int) {
	n, m := ahi-alo, bhi-blo
	delta := n - m
	odd := delta%2 != 0
	// Diagonal k is stored at k+off: the forward paths reach |k| up
	// to (n+m+1)/2, the backward ones that much around delta.
	off := 2*(n+m) + 2
	size := 2*off + 1
	if len(d.vf) < size {
		d.vf, d.vb = make([]int, size), make([]int, size)
	}
	vf, vb := d.vf[:size], d.vb[:size]
	for k := range vf {
		vf[k], vb[k] = -1, -1
	}
	vf[off+1] = 0 // x on diagonal 1 before any step, so the first step reaches diagonal 0
	vb[off+delta-1] = n
	for dist := 0; dist <= (n+m+1)/2; dist++ {
		// Forward paths, diagonals -dist..dist in steps of two.
		for k := -dist; k <= dist; k += 2 {
			var fx int
			if k == -dist || (k != dist && vf[off+k-1] < vf[off+k+1]) {
				fx = vf[off+k+1]
			} else {
				fx = vf[off+k-1] + 1
			}
			fy := fx - k
			sx, sy := fx, fy
			for fx < n && fy < m && d.a[alo+fx] == d.b[blo+fy] {
				fx++
				fy++
			}
			vf[off+k] = fx
			if odd && k-delta >= -(dist-1) && k-delta <= dist-1 && vb[off+k] != -1 && vb[off+k] <= fx {
				return alo + sx, blo + sy, alo + fx, blo + fy
			}
		}
		// Backward paths, diagonals delta+dist..delta-dist, the
		// removal side first, so a tie is broken as diff -u breaks it:
		// removals before insertions.
		for k := dist; k >= -dist; k -= 2 {
			kk := k + delta
			var bx int
			if k == dist || (k != -dist && vb[off+kk-1] < vb[off+kk+1]) {
				bx = vb[off+kk-1]
			} else {
				bx = vb[off+kk+1] - 1
			}
			by := bx - kk
			sx, sy := bx, by
			for bx > 0 && by > 0 && d.a[alo+bx-1] == d.b[blo+by-1] {
				bx--
				by--
			}
			vb[off+kk] = bx
			if !odd && kk >= -dist && kk <= dist && vf[off+kk] != -1 && vf[off+kk] >= bx {
				return alo + bx, blo + by, alo + sx, blo + sy
			}
		}
	}
	// Unreachable: the paths meet within (n+m+1)/2 steps.
	return alo, blo, alo, blo
}
