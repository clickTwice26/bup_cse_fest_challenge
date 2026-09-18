package main

// Generic LP builder: accumulates <=, >=, == rows over structural variables and
// compiles to the standard form gonum's Simplex requires (A x = b, x >= 0, b >= 0)
// by adding slack/surplus columns and negating rows with a negative RHS.

type sense int

const (
	le sense = iota
	ge
	eq
)

type lpRow struct {
	coef map[int]float64
	s    sense
	rhs  float64
}

type lpBuilder struct {
	nStruct int
	rows    []lpRow
	// upper bounds on structural vars, NaN-free; <0 means unbounded
	ub []float64
}

func newLP(nStruct int) *lpBuilder {
	ub := make([]float64, nStruct)
	for i := range ub {
		ub[i] = -1 // unbounded
	}
	return &lpBuilder{nStruct: nStruct, ub: ub}
}

func (b *lpBuilder) setUB(i int, v float64) { b.ub[i] = v }

func (b *lpBuilder) add(coef map[int]float64, s sense, rhs float64) {
	b.rows = append(b.rows, lpRow{coef: coef, s: s, rhs: rhs})
}

// compile returns (A as row-major dense, b, totalVars, nRows).
func (b *lpBuilder) compile() ([]float64, []float64, int, int) {
	rows := make([]lpRow, 0, len(b.rows)+b.nStruct)
	rows = append(rows, b.rows...)
	for i, u := range b.ub {
		if u >= 0 {
			rows = append(rows, lpRow{coef: map[int]float64{i: 1}, s: le, rhs: u})
		}
	}
	nSlack := 0
	for _, r := range rows {
		if r.s != eq {
			nSlack++
		}
	}
	total := b.nStruct + nSlack
	nRow := len(rows)
	A := make([]float64, nRow*total)
	rhs := make([]float64, nRow)
	slack := b.nStruct
	for i, r := range rows {
		sign := 1.0
		if r.rhs < 0 {
			sign = -1.0 // keep b >= 0 so phase-1 stays well posed
		}
		for j, v := range r.coef {
			A[i*total+j] = sign * v
		}
		switch r.s {
		case le:
			A[i*total+slack] = sign * 1.0
			slack++
		case ge:
			A[i*total+slack] = sign * -1.0
			slack++
		}
		rhs[i] = sign * r.rhs
	}
	return A, rhs, total, nRow
}
