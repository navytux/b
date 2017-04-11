// Copyright 2014 The b Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package b

import (
	"fmt"
	"io"
	"sync"
)

const (
	//kx = 32 //TODO benchmark tune this number if using custom key/value type(s).
	//kd = 32 //TODO benchmark tune this number if using custom key/value type(s).
	kx = 2
	kd = 2
)

func init() {
	if kd < 1 {
		panic(fmt.Errorf("kd %d: out of range", kd))
	}

	if kx < 2 {
		panic(fmt.Errorf("kx %d: out of range", kx))
	}
}

var (
	btDPool = sync.Pool{New: func() interface{} { return &d{} }}
	btEPool = btEpool{sync.Pool{New: func() interface{} { return &Enumerator{} }}}
	btTPool = btTpool{sync.Pool{New: func() interface{} { return &Tree{} }}}
	btXPool = sync.Pool{New: func() interface{} { return &x{} }}
)

type btTpool struct{ sync.Pool }

func (p *btTpool) get(cmp Cmp) *Tree {
	x := p.Get().(*Tree)
	x.cmp = cmp
	x.hitDi = -1
	x.hitPi = -1
	return x
}

type btEpool struct{ sync.Pool }

func (p *btEpool) get(err error, hit bool, i int, k interface{} /*K*/, q *d, t *Tree, ver int64) *Enumerator {
	x := p.Get().(*Enumerator)
	x.err, x.hit, x.i, x.k, x.q, x.t, x.ver = err, hit, i, k, q, t, ver
	return x
}

type (
	// Cmp compares a and b. Return value is:
	//
	//	< 0 if a <  b
	//	  0 if a == b
	//	> 0 if a >  b
	//
	Cmp func(a, b interface{} /*K*/) int

	d struct { // data page
		c int
		d [2*kd + 1]de
		n *d
		p *d
	}

	de struct { // d element
		k interface{} /*K*/
		v interface{} /*V*/
	}

	// Enumerator captures the state of enumerating a tree. It is returned
	// from the Seek* methods. The enumerator is aware of any mutations
	// made to the tree in the process of enumerating it and automatically
	// resumes the enumeration at the proper key, if possible.
	//
	// However, once an Enumerator returns io.EOF to signal "no more
	// items", it does no more attempt to "resync" on tree mutation(s).  In
	// other words, io.EOF from an Enumerator is "sticky" (idempotent).
	Enumerator struct {
		err error
		hit bool
		i   int
		k   interface{} /*K*/
		q   *d
		t   *Tree
		ver int64
	}

	// Tree is a B+tree.
	Tree struct {
		c     int
		cmp   Cmp
		first *d
		last  *d
		r     interface{}
		ver   int64

		// information about last data page which Set/Put/Delete modified
		hitD     *d   // data page & pos of last write access
		hitDi    int
		hitP     *x   // parent & pos for data page (= -1 if no parent)
		hitPi    int
		hitKmin  xkey // data page key range is [hitKmin, hitKmax)
		hitKmax  xkey
		hitPKmax xkey // Kmax for whole hitP
	}

	xe struct { // x element
		ch interface{}
		k  interface{} /*K*/
	}

	x struct { // index page
		c int
		x [2*kx + 2]xe
	}

	xkey struct { // key + whether value is present at all
		k    interface {} /*K*/
		kset bool         // if not set - k not present
	}

	//keyrange struct { // key range [kmin, kmax)
	//	kmin xkey // if not set = -∞
	//	kmax xkey // if not set = +∞
	//}
)

var ( // R/O zero values
	zd  d
	zde de
	ze  Enumerator
	zk  interface{} /*K*/
	zt  Tree
	zx  x
	zxe xe
)

func clr(q interface{}) {
	switch x := q.(type) {
	case *x:
		for i := 0; i <= x.c; i++ { // Ch0 Sep0 ... Chn-1 Sepn-1 Chn
			clr(x.x[i].ch)
		}
		*x = zx
		btXPool.Put(x)
	case *d:
		*x = zd
		btDPool.Put(x)
	}
}

func (xk *xkey) set(k interface{} /*K*/) {
	xk.k = k
	xk.kset = true
}

// -------------------------------------------------------------------------- x

func newX(ch0 interface{}) *x {
	r := btXPool.Get().(*x)
	r.x[0].ch = ch0
	return r
}

func (q *x) extract(i int) {
	q.c--
	if i < q.c {
		copy(q.x[i:], q.x[i+1:q.c+1])
		q.x[q.c].ch = q.x[q.c+1].ch
		q.x[q.c].k = zk  // GC
		q.x[q.c+1] = zxe // GC
	}
}

func (q *x) insert(i int, k interface{} /*K*/, ch interface{}) *x {
	//dbg("X.insert %v @%d", q, i)
	c := q.c
	if i < c {
		q.x[c+1].ch = q.x[c].ch
		copy(q.x[i+2:], q.x[i+1:c])
		q.x[i+1].k = q.x[i].k
	}
	c++
	q.c = c
	q.x[i].k = k
	q.x[i+1].ch = ch
	return q
}

func (q *x) siblings(i int) (l, r *d) {
	if i >= 0 {
		if i > 0 {
			l = q.x[i-1].ch.(*d)
		}
		if i < q.c {
			r = q.x[i+1].ch.(*d)
		}
	}
	return
}

// -------------------------------------------------------------------------- d

func (l *d) mvL(r *d, c int) {
	copy(l.d[l.c:], r.d[:c])
	copy(r.d[:], r.d[c:r.c])
	l.c += c
	r.c -= c
}

func (l *d) mvR(r *d, c int) {
	copy(r.d[c:], r.d[:r.c])
	copy(r.d[:c], l.d[l.c-c:])
	r.c += c
	l.c -= c
}

// ----------------------------------------------------------------------- Tree

// TreeNew returns a newly created, empty Tree. The compare function is used
// for key collation.
func TreeNew(cmp Cmp) *Tree {
	return btTPool.get(cmp)
}

// Clear removes all K/V pairs from the tree.
func (t *Tree) Clear() {
	if t.r == nil {
		return
	}

	clr(t.r)
	t.c, t.first, t.last, t.r = 0, nil, nil, nil
	// TODO reset .hitD
	t.ver++
}

// Close performs Clear and recycles t to a pool for possible later reuse. No
// references to t should exist or such references must not be used afterwards.
func (t *Tree) Close() {
	t.Clear()
	*t = zt
	btTPool.Put(t)
}

func (t *Tree) cat(p *x, q, r *d, pi int) {
	// FIXME update hit (called from Delete -> underflow )
	t.ver++
	q.mvL(r, r.c)
	if r.n != nil {
		r.n.p = q
	} else {
		t.last = q
	}
	q.n = r.n
	*r = zd
	btDPool.Put(r)
	if p.c > 1 {
		p.extract(pi)
		p.x[pi].ch = q
		return
	}

	switch x := t.r.(type) {
	case *x:
		*x = zx
		btXPool.Put(x)
	case *d:
		*x = zd
		btDPool.Put(x)
	}
	t.r = q
}

func (t *Tree) catX(p, q, r *x, pi int) {
	t.ver++
	q.x[q.c].k = p.x[pi].k
	copy(q.x[q.c+1:], r.x[:r.c])
	q.c += r.c + 1
	q.x[q.c].ch = r.x[r.c].ch
	*r = zx
	btXPool.Put(r)
	if p.c > 1 {
		p.c--
		pc := p.c
		if pi < pc {
			p.x[pi].k = p.x[pi+1].k
			copy(p.x[pi+1:], p.x[pi+2:pc+1])
			p.x[pc].ch = p.x[pc+1].ch
			p.x[pc].k = zk     // GC
			p.x[pc+1].ch = nil // GC
		}
		return
	}

	switch x := t.r.(type) {
	case *x:
		*x = zx
		btXPool.Put(x)
	case *d:
		*x = zd
		btDPool.Put(x)
	}
	t.r = q
}

// Delete removes the k's KV pair, if it exists, in which case Delete returns
// true.
func (t *Tree) Delete(k interface{} /*K*/) (ok bool) {
	//dbg("--- PRE Delete(%v)\n%s", k, t.dump())
	//defer func() {
	//	dbg("--- POST\n%s\n====\n", t.dump())
	//}()

	// TODO audit for hit update
	pi := -1
	var p *x
	q := t.r
	if q == nil {
		return false
	}

	for {
		var i int
		i, ok = t.find(q, k)
		if ok {
			switch x := q.(type) {
			case *x:
				if x.c < kx && q != t.r {
					x, i = t.underflowX(p, x, pi, i)
				}
				pi = i + 1
				p = x
				q = x.x[pi].ch
				ok = false
				continue
			case *d:
				t.extract(x, i)		// hit
				if x.c >= kd {
					return true
				}

				if q != t.r {
					t.underflow(p, x, pi)	// hit
				} else if t.c == 0 {
					t.Clear()	// hit
				}
				return true
			}
		}

		switch x := q.(type) {
		case *x:
			if x.c < kx && q != t.r {
				x, i = t.underflowX(p, x, pi, i)
			}
			pi = i
			p = x
			q = x.x[i].ch
		case *d:
			return false	// hit
		}
	}
}

func (t *Tree) extract(q *d, i int) { // (r interface{} /*V*/) {
	// XXX update hit ?
	t.ver++
	//r = q.d[i].v // prepared for Extract
	q.c--
	if i < q.c {
		copy(q.d[i:], q.d[i+1:q.c+1])
	}
	q.d[q.c] = zde // GC
	t.c--
	return
}

func (t *Tree) find(q interface{}, k interface{} /*K*/) (i int, ok bool) {
	var mk interface{} /*K*/
	l := 0
	switch x := q.(type) {
	case *x:
		h := x.c - 1
		for l <= h {
			m := (l + h) >> 1
			mk = x.x[m].k
			switch cmp := t.cmp(k, mk); {
			case cmp > 0:
				l = m + 1
			case cmp == 0:
				return m, true
			default:
				h = m - 1
			}
		}
	case *d:
		h := x.c - 1
		for l <= h {
			m := (l + h) >> 1
			mk = x.d[m].k
			switch cmp := t.cmp(k, mk); {
			case cmp > 0:
				l = m + 1
			case cmp == 0:
				return m, true
			default:
				h = m - 1
			}
		}
	}
	return l, false
}

// same as find but when we pre-know range to search and for data-page only
func (t *Tree) find2(d *d, k interface{} /*K*/, l, h int) (i int, ok bool) {
	for l <= h {
		m := (l + h) >> 1
		mk := d.d[m].k
		switch cmp := t.cmp(k, mk); {
		case cmp > 0:
			l = m + 1
		case cmp == 0:
			return m, true
		default:
			h = m - 1
		}
	}
	return l, false
}

// hitFind returns whether k belongs to previosly hit data page XXX text
// if no  -1, false is returned
// if yes returned are:
// - i:  index corresponding to data entry in t.hitD with min(k' : k' >= k)
// - ok: whether k' == k
func (t *Tree) hitFind(k interface{} /*K*/) (i int, ok bool) {
	hit := t.hitD
	if hit == nil {
		return -1, false
	}

	i = t.hitDi
	//p := t.hitP
	//pi := t.hitPi

	switch cmp := t.cmp(k, hit.d[i].k); {
	case cmp > 0:
		// // in hit range: < p.k (which is ∞ when pi == p.c)
		// if p != nil && pi < p.c && t.cmp(k, p.x[pi].k) >= 0 {
		// 	return -1, false
		// }

		if !(t.hitKmax.kset && t.cmp(k, t.hitKmax.k) < 0) {
			return -1, false
		}

		return t.find2(hit, k, i+1, hit.c - 1)

		/*
		h := hit.c - 1
		l := i
		if l < h {
			l++
		}

		return t.find2(hit, k, l, h)
		*/

	case cmp < 0:
		// // in hit range: >= pprev.k
		// if p != nil && pi > 0 && t.cmp(k, p.x[pi-1].k) < 0 {
		// 	return -1, false
		// }

		if !(t.hitKmin.kset && t.cmp(k, t.hitKmin.k) >= 0) {
			return -1, false
		}

		return t.find2(hit, k, 0, i)

	default:
		return i, true;
	}
}

// First returns the first item of the tree in the key collating order, or
// (zero-value, zero-value) if the tree is empty.
func (t *Tree) First() (k interface{} /*K*/, v interface{} /*V*/) {
	if q := t.first; q != nil {
		q := &q.d[0]
		k, v = q.k, q.v
	}
	return
}

// Get returns the value associated with k and true if it exists. Otherwise Get
// returns (zero-value, false).
func (t *Tree) Get(k interface{} /*K*/) (v interface{} /*V*/, ok bool) {
	q := t.r
	if q == nil {
		return
	}

	for {
		var i int
		if i, ok = t.find(q, k); ok {
			switch x := q.(type) {
			case *x:
				q = x.x[i+1].ch
				continue
			case *d:
				return x.d[i].v, true
			}
		}
		switch x := q.(type) {
		case *x:
			q = x.x[i].ch
		default:
			return
		}
	}
}

func (t *Tree) insert(q *d, i int, k interface{} /*K*/, v interface{} /*V*/) *d {
	t.ver++
	c := q.c
	if i < c {
		copy(q.d[i+1:], q.d[i:c])
	}
	c++
	q.c = c
	q.d[i].k, q.d[i].v = k, v
	t.c++
	t.hitD = q
	t.hitDi = i
	return q
}

// Last returns the last item of the tree in the key collating order, or
// (zero-value, zero-value) if the tree is empty.
func (t *Tree) Last() (k interface{} /*K*/, v interface{} /*V*/) {
	if q := t.last; q != nil {
		q := &q.d[q.c-1]
		k, v = q.k, q.v
	}
	return
}

// Len returns the number of items in the tree.
func (t *Tree) Len() int {
	return t.c
}

func (t *Tree) overflow(p *x, q *d, pi, i int, k interface{} /*K*/, v interface{} /*V*/) {
	t.ver++
	l, r := p.siblings(pi)

	if l != nil && l.c < 2*kd && i != 0 {
		l.mvL(q, 1)
		t.insert(q, i-1, k, v)
		p.x[pi-1].k = q.d[0].k
		t.hitKmin.set(q.d[0].k)
		t.hitPi = pi	// XXX already pre-set this way
		t.checkHitP(q)
		return
	}

	if r != nil && r.c < 2*kd {
		if i < 2*kd {
			q.mvR(r, 1)
			t.insert(q, i, k, v)
			p.x[pi].k = r.d[0].k
			t.hitKmax.set(r.d[0].k)
			t.hitPi = pi	// XXX already pre-set this way
			t.checkHitP(q)
			return
		}

		t.insert(r, 0, k, v)
		p.x[pi].k = k

		t.hitKmin.set(k)
		kmax := t.hitPKmax
		if pi + 1 < p.c { // means < ∞
			kmax.set(p.x[pi+1].k)
		}
		t.hitKmax = kmax
		t.hitPi = pi + 1
		t.checkHitP(r)
		return
	}

	t.split(p, q, pi, i, k, v)
}

// Seek returns an Enumerator positioned on an item such that k >= item's key.
// ok reports if k == item.key The Enumerator's position is possibly after the
// last item in the tree.
func (t *Tree) Seek(k interface{} /*K*/) (e *Enumerator, ok bool) {
	q := t.r
	if q == nil {
		e = btEPool.get(nil, false, 0, k, nil, t, t.ver)
		return
	}

	for {
		var i int
		if i, ok = t.find(q, k); ok {
			switch x := q.(type) {
			case *x:
				q = x.x[i+1].ch
				continue
			case *d:
				return btEPool.get(nil, ok, i, k, x, t, t.ver), true
			}
		}

		switch x := q.(type) {
		case *x:
			q = x.x[i].ch
		case *d:
			return btEPool.get(nil, ok, i, k, x, t, t.ver), false
		}
	}
}

// SeekFirst returns an enumerator positioned on the first KV pair in the tree,
// if any. For an empty tree, err == io.EOF is returned and e will be nil.
func (t *Tree) SeekFirst() (e *Enumerator, err error) {
	q := t.first
	if q == nil {
		return nil, io.EOF
	}

	return btEPool.get(nil, true, 0, q.d[0].k, q, t, t.ver), nil
}

// SeekLast returns an enumerator positioned on the last KV pair in the tree,
// if any. For an empty tree, err == io.EOF is returned and e will be nil.
func (t *Tree) SeekLast() (e *Enumerator, err error) {
	q := t.last
	if q == nil {
		return nil, io.EOF
	}

	return btEPool.get(nil, true, q.c-1, q.d[q.c-1].k, q, t, t.ver), nil
}

// verify that t.hit* are computed ok
// XXX -> all_test

// Set sets the value associated with k.
func (t *Tree) Set(k interface{} /*K*/, v interface{} /*V*/) {
	//dbg("--- PRE Set(%v, %v)\t(%v @%d, [%v, %v)  PKmax: %v)\n%s", k, v, t.hitD, t.hitDi, t.hitKmin, t.hitKmax, t.hitPKmax, t.dump())
	defer t.checkHit(k)
	//defer func() {
	//	dbg("--- POST\n%s\n====\n", t.dump())
	//}()


	// check if we can do the update nearby previous change
	i, ok := t.hitFind(k)
	if i >= 0 {
		//dbg("hit found\t-> %d, %v", i, ok)
		dd := t.hitD

		switch {
		case ok:
			//dbg("ok'")
			dd.d[i].v = v
			t.hitDi = i
			return

		case dd.c < 2*kd:
			//dbg("insert'")
			t.insert(dd, i, k, v)
			return

		// here: need to overflow but we have to check: if overflowing
		// would cause upper level overflow -> we cannot overflow here -
		// - need to do the usual scan from root to split index pages.
		default:
			//break
			p, pi := t.hitP, t.hitPi
			if p == nil || p.c <= 2*kx {	// XXX < vs <=
				//dbg("overflow'")
				t.overflow(p, dd, pi, i, k, v)
				return
			}
		}
	}

	// data page not quickly found - search and descent from root
	pi := -1
	var p *x
	q := t.r
	if q == nil {
		//dbg("empty")
		z := t.insert(btDPool.Get().(*d), 0, k, v) // XXX update hit
		t.r, t.first, t.last = z, z, z
		return
	}

	var hitKmin, hitKmax xkey // initially [-∞, +∞)
	var hitPKmax xkey         // Kmax for whole hitP

	for {
		i, ok = t.find(q, k)
		switch x := q.(type) {
		case *x:
			//hitPKmax = hitKmax

			if x.c > 2*kx {
				//x, i = t.splitX(p, x, pi, i)
				//dbg("splitX")
				x, i, p, pi = t.splitX(p, x, pi, i)

				// NOTE splitX changes p which means hit
				// Kmin/Kmax/PKmax have to be recomputed
				if pi >= 0 && pi < p.c {
					hitPKmax.set(p.x[pi].k) // XXX wrong vs oo and not oo above
					//dbg("hitPKmax X: %v", hitPKmax)
					hitKmax = hitPKmax
					//dbg("hitKmax X: %v", hitKmax)
				}

				if pi > 0 {
					hitKmin.set(p.x[pi-1].k)	// XXX also recheck vs above
					//dbg("hitKmin X: %v", hitKmin)
				}
			} else {
				// p unchanged
				// FIXME move to above without else
				hitPKmax = hitKmax
			}

			p = x
			pi = i
			if ok {
				pi++
			}

			q = p.x[pi].ch

			if pi > 0 {	// XXX also check < p.c ?
				hitKmin.set(p.x[pi-1].k)
				//dbg("hitKmin: %v", hitKmin)
			}

			if pi < p.c {
				hitKmax.set(p.x[pi].k)
				//dbg("hitKmax: %v", hitKmax)
			}


		case *d:
			// data page found - perform the update
			t.hitP = p
			t.hitPi = pi
			t.hitKmin = hitKmin
			t.hitKmax = hitKmax
			t.hitPKmax = hitPKmax

			switch {
			case ok:
				//dbg("ok")
				x.d[i].v = v
				t.hitD, t.hitDi = x, i

			case x.c < 2*kd:
				//dbg("insert")
				t.insert(x, i, k, v)

			default:
				//dbg("overflow")
				// NOTE overflow will correct hit Kmin, Kmax, P and Pi as needed
				t.overflow(p, x, pi, i, k, v)
			}

			return
		}
	}
}

// Put combines Get and Set in a more efficient way where the tree is walked
// only once. The upd(ater) receives (old-value, true) if a KV pair for k
// exists or (zero-value, false) otherwise. It can then return a (new-value,
// true) to create or overwrite the existing value in the KV pair, or
// (whatever, false) if it decides not to create or not to update the value of
// the KV pair.
//
// 	tree.Set(k, v) call conceptually equals calling
//
// 	tree.Put(k, func(interface{} /*K*/, bool){ return v, true })
//
// modulo the differing return values.
func (t *Tree) Put(k interface{} /*K*/, upd func(oldV interface{} /*V*/, exists bool) (newV interface{} /*V*/, write bool)) (oldV interface{} /*V*/, written bool) {
	pi := -1
	var p *x
	q := t.r
	var newV interface{} /*V*/
	if q == nil {
		// new KV pair in empty tree
		newV, written = upd(newV, false)
		if !written {
			return
		}

		z := t.insert(btDPool.Get().(*d), 0, k, newV)
		t.r, t.first, t.last = z, z, z
		return
	}

	// TODO handle t.hitD

	for {
		i, ok := t.find(q, k)
		if ok {
			switch x := q.(type) {
			case *x:
				if x.c > 2*kx {
					panic("TODO")
					x, i, _, _ = t.splitX(p, x, pi, i)
				}
				pi = i + 1
				p = x
				q = x.x[i+1].ch
				continue
			case *d:
				oldV = x.d[i].v
				newV, written = upd(oldV, true)
				if !written {
					return
				}

				// XXX update hit

				x.d[i].v = newV
			}
			return
		}

		switch x := q.(type) {
		case *x:
			if x.c > 2*kx {
				panic("TODO")
				//x, i = t.splitX(p, x, pi, i)
			}
			pi = i
			p = x
			q = x.x[i].ch
		case *d: // new KV pair
			newV, written = upd(newV, false)
			if !written {
				return
			}

			// XXX update hit

			switch {
			case x.c < 2*kd:
				t.insert(x, i, k, newV)
			default:
				//t.overflow(p, x, pi, i, k, newV)
				panic("TODO")
			}
			return
		}
	}
}

func (t *Tree) checkHitP(q *d) {
	p := t.hitP
	pi := t.hitPi
	if p.x[t.hitPi].ch != q {
		println()
		dbg("BUG: HITP MISMATCH:")
		dbg("hitP: %v  @%d", p, pi)
		dbg("q: %p", q)
		println()
		panic(0)
	}
}

func (t *Tree) split(p *x, q *d, pi, i int, k interface{} /*K*/, v interface{} /*V*/) {
	t.ver++
	r := btDPool.Get().(*d)
	if q.n != nil {
		r.n = q.n
		r.n.p = r
	} else {
		t.last = r
	}
	q.n = r
	r.p = q

	copy(r.d[:], q.d[kd:2*kd])
	for i := range q.d[kd:] {
		q.d[kd+i] = zde
	}
	q.c = kd
	r.c = kd

	if pi >= 0 {
		p.insert(pi, r.d[0].k, r)
	} else {
		p = newX(q).insert(0, r.d[0].k, r)
		pi = 0
		t.r = p
		t.hitP = p
	}

	if i > kd {
		t.insert(r, i-kd, k, v)
		t.hitKmin.set(p.x[pi].k)
		kmax := t.hitPKmax
		if pi + 1 < p.c {
			kmax.set(p.x[pi+1].k)
		}
		t.hitKmax = kmax
		t.hitPi = pi + 1
		t.checkHitP(r)
	} else {
		t.insert(q, i, k, v)
		t.hitKmax.set(r.d[0].k)
		t.hitPi = pi	// XXX already pre-set so
		t.checkHitP(q)
	}
}

func (t *Tree) splitX(p *x, q *x, pi int, i int) (*x, int, *x, int) {
	t.ver++
	r := btXPool.Get().(*x)
	copy(r.x[:], q.x[kx+1:])
	q.c = kx
	r.c = kx
	if pi >= 0 {
		p.insert(pi, q.x[kx].k, r)
		q.x[kx].k = zk
		for i := range q.x[kx+1:] {
			q.x[kx+i+1] = zxe
		}

		switch {
		case i < kx:
			return q, i, p, pi
		case i == kx:
			return p, pi, p, -1
		default: // i > kx
			return r, i - kx - 1, p, pi + 1
		}
	}

	nr := newX(q).insert(0, q.x[kx].k, r)
	t.r = nr
	q.x[kx].k = zk
	for i := range q.x[kx+1:] {
		q.x[kx+i+1] = zxe
	}

	switch {
	case i < kx:
		return q, i, nr, 0
	case i == kx:
		return nr, 0, nr, -1
	default: // i > kx
		return r, i - kx - 1, nr, 1
	}
}

func (t *Tree) underflow(p *x, q *d, pi int) {
	t.ver++
	l, r := p.siblings(pi)

	if l != nil && l.c+q.c >= 2*kd {
		l.mvR(q, 1)
		// TODO update t.hitD = q @ i
		p.x[pi-1].k = q.d[0].k
		return
	}

	if r != nil && q.c+r.c >= 2*kd {
		q.mvL(r, 1)
		// TODO update t.hitD = q @ i
		p.x[pi].k = r.d[0].k
		r.d[r.c] = zde // GC
		return
	}

	if l != nil {
		t.cat(p, l, q, pi-1)
		return
	}

	t.cat(p, q, r, pi)
}

func (t *Tree) underflowX(p *x, q *x, pi int, i int) (*x, int) {
	t.ver++
	var l, r *x

	if pi >= 0 {
		if pi > 0 {
			l = p.x[pi-1].ch.(*x)
		}
		if pi < p.c {
			r = p.x[pi+1].ch.(*x)
		}
	}

	if l != nil && l.c > kx {
		q.x[q.c+1].ch = q.x[q.c].ch
		copy(q.x[1:], q.x[:q.c])
		q.x[0].ch = l.x[l.c].ch
		q.x[0].k = p.x[pi-1].k
		q.c++
		i++
		l.c--
		p.x[pi-1].k = l.x[l.c].k
		return q, i
	}

	if r != nil && r.c > kx {
		q.x[q.c].k = p.x[pi].k
		q.c++
		q.x[q.c].ch = r.x[0].ch
		p.x[pi].k = r.x[0].k
		copy(r.x[:], r.x[1:r.c])
		r.c--
		rc := r.c
		r.x[rc].ch = r.x[rc+1].ch
		r.x[rc].k = zk
		r.x[rc+1].ch = nil
		return q, i
	}

	if l != nil {
		i += l.c + 1
		t.catX(p, l, q, pi-1)
		q = l
		return q, i
	}

	t.catX(p, q, r, pi)
	return q, i
}

// ----------------------------------------------------------------- Enumerator

// Close recycles e to a pool for possible later reuse. No references to e
// should exist or such references must not be used afterwards.
func (e *Enumerator) Close() {
	*e = ze
	btEPool.Put(e)
}

// Next returns the currently enumerated item, if it exists and moves to the
// next item in the key collation order. If there is no item to return, err ==
// io.EOF is returned.
func (e *Enumerator) Next() (k interface{} /*K*/, v interface{} /*V*/, err error) {
	if err = e.err; err != nil {
		return
	}

	if e.ver != e.t.ver {
		f, _ := e.t.Seek(e.k)
		*e = *f
		f.Close()
	}
	if e.q == nil {
		e.err, err = io.EOF, io.EOF
		return
	}

	if e.i >= e.q.c {
		if err = e.next(); err != nil {
			return
		}
	}

	i := e.q.d[e.i]
	k, v = i.k, i.v
	e.k, e.hit = k, true
	e.next()
	return
}

func (e *Enumerator) next() error {
	if e.q == nil {
		e.err = io.EOF
		return io.EOF
	}

	switch {
	case e.i < e.q.c-1:
		e.i++
	default:
		if e.q, e.i = e.q.n, 0; e.q == nil {
			e.err = io.EOF
		}
	}
	return e.err
}

// Prev returns the currently enumerated item, if it exists and moves to the
// previous item in the key collation order. If there is no item to return, err
// == io.EOF is returned.
func (e *Enumerator) Prev() (k interface{} /*K*/, v interface{} /*V*/, err error) {
	if err = e.err; err != nil {
		return
	}

	if e.ver != e.t.ver {
		f, _ := e.t.Seek(e.k)
		*e = *f
		f.Close()
	}
	if e.q == nil {
		e.err, err = io.EOF, io.EOF
		return
	}

	if !e.hit {
		// move to previous because Seek overshoots if there's no hit
		if err = e.prev(); err != nil {
			return
		}
	}

	if e.i >= e.q.c {
		if err = e.prev(); err != nil {
			return
		}
	}

	i := e.q.d[e.i]
	k, v = i.k, i.v
	e.k, e.hit = k, true
	e.prev()
	return
}

func (e *Enumerator) prev() error {
	if e.q == nil {
		e.err = io.EOF
		return io.EOF
	}

	switch {
	case e.i > 0:
		e.i--
	default:
		if e.q = e.q.p; e.q == nil {
			e.err = io.EOF
			break
		}

		e.i = e.q.c - 1
	}
	return e.err
}
