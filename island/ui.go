package island

import (
	"fmt"
	"image"
	"math"
	"sort"
	"strings"
	"time"
)

type Button struct {
	Key, Label      string
	Primary, Danger bool
}

type Option struct{ Label, Desc string }

// Card is anything that wants the user's eyes: a permission request, a
// question, an edge nudge, a finished session.
type Card struct {
	ID       string
	Kind     string // perm | ask | edge | done | info
	Mood     mood
	Title    string
	Meta     string
	Body     string
	Code     string
	Plus     int
	Minus    int
	Buttons  []Button
	Options  []Option
	Multi    bool
	Footer   string // left-aligned ghost action, e.g. "In terminal"
	Expires  time.Time
	Priority int
	OnPick   func(key string, picked []int)
	OnExpire func()

	picked map[int]bool
	shown  time.Time
}

type Session struct {
	ID, Project, State, Label string // State: work | wait | idle
	Plus, Minus               int
	Since, Seen               time.Time
	Pids                      []int
}

type EdgeStatus struct {
	Today, Need, Streak int
	Closed              bool
	Task, TaskURL       string
	Started             time.Time
	Pending             bool
}

type view int

const (
	vHidden view = iota
	vPip
	vCompact
	vCard
	vPanel
)

type hit struct {
	r   image.Rectangle
	key string
}

// UI owns all state; only the main loop touches it.
type UI struct {
	W, H     int
	cv       *canvas
	pal      palette
	sessions map[string]*Session
	cards    []*Card
	edge     EdgeStatus

	hover      bool
	leftAt     time.Time
	mx, my     float64
	hoverKey   string
	hits       []hit
	onKey      func(key string)
	shapeW, vw spring
	shapeH, vh spring
	lastView   view
	viewSince  time.Time
	start      time.Time
	lastBlink  time.Time
	bounceAt   time.Time
	lastFrame  time.Time
	shape      image.Rectangle
	settledAt  time.Time
	dirty      bool        // state changed: the next frame repaints everything
	base       *image.RGBA // last full frame without Ember
	emberRect  image.Rectangle
	emberArgs  [3]float32 // cx, cy, size
	minute     time.Time
	lastShape  image.Rectangle
	now        func() time.Time
}

type spring struct{ x, v float64 }

func (s *spring) step(target, dt float64) {
	const k, d = 300.0, 30.0
	s.v += ((target-s.x)*k - s.v*d) * dt
	s.x += s.v * dt
}

func (s *spring) settled(target float64) bool {
	return math.Abs(target-s.x) < 0.4 && math.Abs(s.v) < 4
}

func NewUI(w, h int) *UI {
	u := &UI{W: w, H: h, cv: newCanvas(w, h), pal: loadPalette(), sessions: map[string]*Session{}, now: time.Now, dirty: true,
		base: image.NewRGBA(image.Rect(0, 0, w, h))}
	u.start = u.now()
	u.lastBlink = u.start
	return u
}

// ---- state changes ----

func (u *UI) AddCard(c *Card) {
	for i, old := range u.cards {
		if old.ID == c.ID {
			u.cards[i] = c
			c.shown = u.now()
			return
		}
	}
	c.shown = u.now()
	u.cards = append(u.cards, c)
	sort.SliceStable(u.cards, func(i, j int) bool { return u.cards[i].Priority > u.cards[j].Priority })
}

func (u *UI) RemoveCard(id string) {
	for i, c := range u.cards {
		if c.ID == id {
			u.cards = append(u.cards[:i], u.cards[i+1:]...)
			return
		}
	}
}

func (u *UI) top() *Card {
	if len(u.cards) == 0 {
		return nil
	}
	return u.cards[0]
}

func (u *UI) Bounce() { u.bounceAt = u.now() }

// expire drops timed-out cards; returns true if anything changed.
func (u *UI) expire() bool {
	now, changed := u.now(), false
	kept := u.cards[:0]
	for _, c := range u.cards {
		if !c.Expires.IsZero() && now.After(c.Expires) {
			if c.OnExpire != nil {
				c.OnExpire()
			}
			changed = true
			continue
		}
		kept = append(kept, c)
	}
	u.cards = kept
	return changed
}

func (u *UI) activeSessions() []*Session {
	var out []*Session
	for _, s := range u.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := stateRank(out[i].State), stateRank(out[j].State)
		if ri != rj {
			return ri > rj
		}
		return out[i].Seen.After(out[j].Seen)
	})
	return out
}

func stateRank(s string) int {
	return map[string]int{"wait": 3, "work": 2, "idle": 1}[s]
}

func (u *UI) currentView() view {
	if u.top() != nil {
		return vCard
	}
	if u.hover || u.now().Sub(u.leftAt) < 350*time.Millisecond {
		return vPanel
	}
	for _, s := range u.sessions {
		if s.State == "work" || s.State == "wait" {
			return vCompact
		}
	}
	if u.edge.Task != "" || u.edge.Closed {
		return vCompact
	}
	return vPip
}

// mood is what Ember feels right now.
func (u *UI) mood() mood {
	if c := u.top(); c != nil {
		return c.Mood
	}
	best := moodIdle
	for _, s := range u.sessions {
		switch s.State {
		case "wait":
			return moodWait
		case "work":
			best = moodWork
		}
	}
	if best == moodIdle && u.edge.Task != "" && !u.edge.Pending {
		best = moodWork
	}
	return best
}

// ---- layout & render ----

const (
	padTop  = 3
	cardW   = 384
	panelW  = 384
	pipW    = 34
	pipH    = 20
	compH   = 28
	rowH    = 30
	btnH    = 28
	padX    = 14
	maxComp = 372
)

// Invalidate marks the state changed.
func (u *UI) Invalidate() { u.dirty = true }

// render draws one frame and reports whether anything is still moving.
// When only Ember changes, it restores its square from the cached frame and
// repaints just that.
func (u *UI) render() (animating bool) {
	animating, _ = u.renderDamage()
	return animating
}

// renderDamage is render plus the rectangle that changed.
func (u *UI) renderDamage() (animating bool, damage image.Rectangle) {
	now := u.now()
	dt := now.Sub(u.lastFrame).Seconds()
	if dt <= 0 || dt > 0.05 {
		dt = 1.0 / 60
	}
	u.lastFrame = now
	v := u.currentView()
	if v != u.lastView {
		u.lastView, u.viewSince, u.dirty = v, now, true
	}
	if m := now.Truncate(time.Minute); !m.Equal(u.minute) { // clocks in the text
		u.minute, u.dirty = m, true
	}
	tw, th := u.measure(v)
	if u.shapeW.x == 0 {
		u.shapeW.x, u.shapeH.x = tw, th
	}
	u.shapeW.step(tw, dt)
	u.shapeH.step(th, dt)
	settled := u.shapeW.settled(tw) && u.shapeH.settled(th)
	if settled {
		u.shapeW.x, u.shapeH.x, u.shapeW.v, u.shapeH.v = tw, th, 0, 0
	}
	// Content fades in once the shape has arrived.
	if !settled {
		u.settledAt = time.Time{}
	} else if u.settledAt.IsZero() {
		u.settledAt = now
	}
	fade := float32(0)
	if settled {
		fade = float32(math.Min(1, now.Sub(u.settledAt).Seconds()/0.12))
	}
	w, h := float32(u.shapeW.x), float32(u.shapeH.x)
	x, y := (float32(u.W)-w)/2, float32(padTop)

	full := u.dirty || !settled || fade < 1
	if full {
		u.dirty = false
		u.cv.clear(u.cv.img.Rect)
		u.hits = u.hits[:0]
		p := u.pal
		rad := min(h/2, 18)
		if v == vCard || v == vPanel {
			rad = min(rad, 16)
		}
		u.cv.shadow(x, y, w, h, rad, 0.55)
		u.cv.roundRectHole(x-1, y-1, w+2, h+2, rad+1, alpha(p.line, 0.9), insetRect(x, y, w, h, rad))
		u.cv.roundRect(x, y, w, h, rad, p.bg)
		u.shape = image.Rect(int(x)-1, int(y)-1, int(x+w)+2, int(y+h)+2)
		switch v {
		case vPip:
			u.emberArgs = [3]float32{x + w/2, y + h/2, 12}
			u.hits = append(u.hits, hit{u.shape, "open"})
		case vCompact:
			u.emberArgs = [3]float32{x + 15, y + h/2 + 0.5, 15}
			if fade > 0 {
				u.drawCompact(x, y, w, h, fade)
			}
			u.hits = append(u.hits, hit{u.shape, "open"})
		case vCard:
			u.emberArgs = [3]float32{x + padX + 9, y + padX + 9, 19}
			if fade > 0 {
				u.drawCard(u.top(), x, y, w, fade)
			}
		case vPanel:
			u.emberArgs = [3]float32{x + padX + 9, y + padX + 9, 19}
			if fade > 0 {
				u.drawPanel(x, y, w, fade)
			}
			u.hits = append(u.hits, hit{u.shape, "panel"})
		}
		copy(u.base.Pix, u.cv.img.Pix)
		// the shape (and its shadow) now, plus wherever it was last frame
		damage = u.shape.Inset(-12).Union(u.lastShape).Intersect(u.cv.img.Rect)
		u.lastShape = u.shape.Inset(-12)
		cx, cy, s := u.emberArgs[0], u.emberArgs[1], u.emberArgs[2]
		u.emberRect = image.Rect(int(cx-s*0.75), int(cy-s*1.05), int(cx+s*0.75)+1, int(cy+s*0.7)+1).Intersect(u.cv.img.Rect)
	} else {
		// restore Ember's square from the cached frame
		r := u.emberRect
		for yy := r.Min.Y; yy < r.Max.Y; yy++ {
			o := yy*u.base.Stride + r.Min.X*4
			copy(u.cv.img.Pix[o:o+r.Dx()*4], u.base.Pix[o:o+r.Dx()*4])
		}
		damage = r
	}

	t := now.Sub(u.start).Seconds()
	blink := float32(0)
	if bs := now.Sub(u.lastBlink).Seconds(); bs < 0.16 {
		blink = float32(math.Sin(bs / 0.16 * math.Pi))
	} else if bs > 4.2 {
		u.lastBlink = now
	}
	bounce := float32(0)
	if bt := now.Sub(u.bounceAt).Seconds(); bt < 0.7 {
		bounce = float32(math.Abs(math.Sin(bt/0.7*2*math.Pi)) * (1 - bt/0.7))
	}
	u.cv.ember(u.pal, u.emberArgs[0], u.emberArgs[1], u.emberArgs[2], u.mood(), t, blink, bounce)
	return !settled || fade < 1 || blink > 0 || bounce > 0, damage
}

// nextBlink is when Ember next needs a frame by itself.
func (u *UI) nextBlink() time.Time { return u.lastBlink.Add(4200 * time.Millisecond) }

// continuous says whether Ember is animating on its own (flicker); the loop
// then ticks at a low rate instead of every frame.
func (u *UI) continuous() bool {
	m := u.mood()
	return m == moodWork || m == moodWait
}

func (u *UI) measure(v view) (float64, float64) {
	switch v {
	case vPip:
		return pipW, pipH
	case vCompact:
		l, r := u.compactText()
		w := 30 + float64(measure(face(fMedium, 12.5), l.text))
		if l.locked {
			w += 15
		}
		if l.plus > 0 || l.minus > 0 {
			w += float64(measure(face(fMedium, 12), diffText(l.plus, l.minus))) + 6
		}
		if r != "" {
			w += 18 + float64(measure(face(fRegular, 12), r))
			if u.edge.Closed && u.edge.Task == "" {
				w += 13
			}
		}
		return math.Min(maxComp, math.Max(110, w+14)), compH
	case vCard:
		return cardW, float64(u.cardHeight(u.top()))
	case vPanel:
		n := min(len(u.sessions), 4)
		h := 48.0
		if u.edge.Task != "" {
			h += rowH + 4
		}
		if n == 0 {
			h += 24
		} else {
			h += float64(n) * rowH
		}
		return panelW, h + 8
	}
	return 0, 0
}

type label struct {
	text        string
	plus, minus int
	mood        mood
	locked      bool
}

func diffText(p, m int) string {
	switch {
	case p > 0 && m > 0:
		return fmt.Sprintf("+%d −%d", p, m)
	case p > 0:
		return fmt.Sprintf("+%d", p)
	}
	return fmt.Sprintf("−%d", m)
}

// compactText picks the one line worth showing, plus edge's chip.
func (u *UI) compactText() (label, string) {
	var l label
	ss := u.activeSessions()
	if len(ss) > 0 && ss[0].State != "idle" {
		s := ss[0]
		switch s.State {
		case "wait":
			l = label{text: "Waiting for you · " + s.Project, mood: moodWait}
		default:
			l = label{text: s.Label, plus: s.Plus, minus: s.Minus, mood: moodWork}
			if l.text == "" {
				l.text = "Thinking"
			}
		}
		busy := 0
		for _, x := range ss {
			if x.State != "idle" {
				busy++
			}
		}
		if busy > 1 {
			l.text = fmt.Sprintf("%s  ·  +%d", l.text, busy-1)
		}
	}
	e := u.edge
	chip := ""
	switch {
	case e.Task != "" && e.Pending:
		chip = "Problem ready"
	case e.Task != "":
		chip = fmt.Sprintf("%s · %s", short(e.Task), mins(u.now().Sub(e.Started)))
	case e.Closed:
		chip = fmt.Sprintf("%d/%d today", e.Today, e.Need)
	}
	if l.text == "" {
		l.text, chip = chip, ""
		l.locked = e.Closed && e.Task == ""
		if e.Task != "" && !e.Pending {
			l.text = fmt.Sprintf("%s · %s", e.Task, mins(u.now().Sub(e.Started)))
		}
	}
	return l, chip
}

func short(task string) string {
	if i := strings.Index(task, " · "); i > 0 {
		return task[:i]
	}
	return task
}

func mins(d time.Duration) string {
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}

func (u *UI) drawCompact(x, y, w, h, fade float32) {
	p := u.pal
	l, chip := u.compactText()
	f := face(fMedium, 12.5)
	tx := x + 29
	base := y + h/2 + 4.5
	avail := w - 29 - 14
	if chip != "" {
		avail -= measure(face(fRegular, 12), chip) + 18
		if u.edge.Closed && u.edge.Task == "" {
			avail -= 13
		}
	}
	txt := l.text
	diff := ""
	if l.plus > 0 || l.minus > 0 {
		diff = diffText(l.plus, l.minus)
		avail -= measure(face(fMedium, 12), diff) + 6
	}
	if l.locked {
		u.cv.lock(tx, y+h/2-6, 11, alpha(p.yellow, float64(fade)))
		tx += 15
		avail -= 15
	}
	txt = ellipsize(f, txt, avail)
	col := p.fg
	if l.mood == moodWait {
		col = p.blue
	}
	tx += u.cv.text(f, txt, tx, base, alpha(col, float64(fade)))
	if diff != "" {
		tx += 6
		if l.plus > 0 {
			tx += u.cv.text(face(fMedium, 12), fmt.Sprintf("+%d", l.plus), tx, base, alpha(p.green, float64(fade)))
			tx += 4
		}
		if l.minus > 0 {
			u.cv.text(face(fMedium, 12), fmt.Sprintf("−%d", l.minus), tx, base, alpha(p.red, float64(fade)))
		}
	}
	if chip != "" {
		cf := face(fRegular, 12)
		cx := x + w - 14 - measure(cf, chip)
		ccol := p.muted
		if u.edge.Pending {
			ccol = p.green
		}
		u.cv.text(cf, chip, cx, base, alpha(ccol, float64(fade)))
		if u.edge.Closed && u.edge.Task == "" {
			u.cv.lock(cx-13, y+h/2-5, 10, alpha(p.yellow, float64(fade)))
		}
		u.cv.circle(cx-9, y+h/2, 1.5, alpha(p.faint, float64(fade)))
	}
}

func (u *UI) cardHeight(c *Card) int {
	if c == nil {
		return 60
	}
	h := padX + 22 // header
	inner := float32(cardW - 2*padX)
	if c.Body != "" {
		h += 6 + 17*len(wrap(face(fRegular, 12.5), c.Body, inner, 3))
	}
	if c.Code != "" {
		h += 8 + 16 + 15*len(codeLines(c.Code, inner-16))
	}
	if len(c.Options) > 0 {
		h += 8 + len(c.Options)*(rowH+5) - 5
	}
	if len(c.Buttons) > 0 || c.Footer != "" {
		h += 12 + btnH
	}
	return min(h+padX, u.H-12)
}

func codeLines(code string, w float32) []string {
	f := face(fMono, 11.5)
	var out []string
	for _, ln := range strings.Split(strings.TrimSpace(code), "\n") {
		out = append(out, ellipsize(f, ln, w))
		if len(out) == 3 {
			break
		}
	}
	return out
}

func (u *UI) drawCard(c *Card, x, y, w, fade float32) {
	p := u.pal
	a := float64(fade)
	inner := w - 2*padX
	// header
	titleX := x + padX + 26
	metaW := float32(0)
	if c.Meta != "" {
		mf := face(fRegular, 11.5)
		meta := ellipsize(mf, c.Meta, 130)
		metaW = measure(mf, meta)
		mx := x + w - padX - metaW
		if c.Kind == "info" || c.Kind == "done" || c.Kind == "edge" {
			mx -= 20
		}
		u.cv.text(mf, meta, mx, y+padX+13.5, alpha(p.muted, a))
	}
	tf := face(fSemiBold, 13.5)
	u.cv.text(tf, ellipsize(tf, c.Title, inner-26-metaW-28), titleX, y+padX+14, alpha(p.fg, a))
	if c.Kind == "info" || c.Kind == "done" || c.Kind == "edge" {
		cx, cy := x+w-padX-6, y+padX+9
		key := "close:" + c.ID
		col := p.faint
		if u.hoverKey == key {
			u.cv.circle(cx, cy, 10, alpha(p.raised, a))
			col = p.fg
		}
		u.cv.text(face(fRegular, 14), "×", cx-4, cy+4.5, alpha(col, a))
		u.hits = append(u.hits, hit{image.Rect(int(cx-11), int(cy-11), int(cx+11), int(cy+11)), key})
	}
	cy := y + padX + 22
	if c.Body != "" {
		cy += 6
		for _, ln := range wrap(face(fRegular, 12.5), c.Body, inner, 3) {
			cy += 17
			u.cv.text(face(fRegular, 12.5), ln, x+padX, cy-4, alpha(mix(p.fg, p.bg, 0.15), a))
		}
	}
	if c.Code != "" {
		lines := codeLines(c.Code, inner-16)
		bh := float32(16 + 15*len(lines))
		cy += 8
		u.cv.roundRect(x+padX, cy, inner, bh, 9, alpha(p.raised, a))
		for i, ln := range lines {
			u.cv.text(face(fMono, 11.5), ln, x+padX+8, cy+8+11+float32(i)*15, alpha(p.fg, a))
		}
		cy += bh
	}
	if len(c.Options) > 0 {
		cy += 8
		for i, o := range c.Options {
			key := fmt.Sprintf("opt:%d", i)
			r := image.Rect(int(x+padX), int(cy), int(x+padX+inner), int(cy+rowH))
			bg := p.raised
			if u.hoverKey == key {
				bg = p.line
			}
			u.cv.roundRect(x+padX, cy, inner, rowH, 10, alpha(bg, a))
			lx := x + padX + 12
			if c.Multi {
				on := c.picked[i]
				u.cv.roundRect(lx, cy+rowH/2-6, 12, 12, 4, alpha(p.line, a))
				if on {
					u.cv.roundRect(lx+2, cy+rowH/2-4, 8, 8, 2.5, alpha(p.green, a))
				}
				lx += 20
			}
			lf := face(fMedium, 12.5)
			lw := u.cv.text(lf, ellipsize(lf, o.Label, inner*0.55), lx, cy+rowH/2+4.5, alpha(p.fg, a))
			if o.Desc != "" {
				df := face(fRegular, 11.5)
				u.cv.text(df, ellipsize(df, o.Desc, x+padX+inner-12-(lx+lw+10)), lx+lw+10, cy+rowH/2+4.5, alpha(p.muted, a))
			}
			u.hits = append(u.hits, hit{r, key})
			cy += rowH + 5
		}
		cy -= 5
	}
	if len(c.Buttons) > 0 || c.Footer != "" {
		cy += 12
		bx := x + w - padX
		for i := len(c.Buttons) - 1; i >= 0; i-- {
			b := c.Buttons[i]
			bf := face(fMedium, 12.5)
			if b.Primary {
				bf = face(fSemiBold, 12.5)
			}
			bw := measure(bf, b.Label) + 28
			bx -= bw
			key := "btn:" + b.Key
			hov := u.hoverKey == key
			fill, txt := p.raised, p.fg
			switch {
			case b.Primary:
				fill, txt = p.green, p.ink
				if hov {
					fill = mix(p.green, p.fg, 0.25)
				}
			case b.Danger:
				txt = p.red
				if hov {
					fill = mix(p.raised, p.red, 0.16)
				}
			default:
				if hov {
					fill = p.line
				}
			}
			u.cv.roundRect(bx, cy, bw, btnH, btnH/2, alpha(fill, a))
			u.cv.text(bf, b.Label, bx+14, cy+btnH/2+4.5, alpha(txt, a))
			u.hits = append(u.hits, hit{image.Rect(int(bx), int(cy), int(bx+bw), int(cy+btnH)), key})
			bx -= 8
		}
		if c.Footer != "" {
			ff := face(fRegular, 12)
			key := "btn:footer"
			col := p.muted
			if u.hoverKey == key {
				col = p.fg
			}
			fw := u.cv.text(ff, c.Footer, x+padX+2, cy+btnH/2+4.5, alpha(col, a))
			u.hits = append(u.hits, hit{image.Rect(int(x+padX), int(cy), int(x+padX+fw+4), int(cy+btnH)), key})
		}
	}
}

func (u *UI) drawPanel(x, y, w, fade float32) {
	p := u.pal
	a := float64(fade)
	e := u.edge
	u.cv.text(face(fSemiBold, 13.5), "edge", x+padX+26, y+padX+14, alpha(p.fg, a))
	sf := face(fRegular, 12)
	sum := fmt.Sprintf("today %d/%d  ·  streak %d", e.Today, e.Need, e.Streak)
	sw := measure(sf, sum)
	sx := x + w - padX - sw
	u.cv.text(sf, sum, sx, y+padX+13.5, alpha(p.muted, a))
	if e.Closed {
		u.cv.lock(sx-16, y+padX+4, 11, alpha(p.yellow, a))
	}
	cy := y + 44
	if e.Task != "" {
		u.cv.roundRect(x+padX-4, cy, w-2*padX+8, rowH, 10, alpha(p.raised, a))
		tf := face(fMedium, 12.5)
		right := ""
		if e.Pending {
			bw := measure(face(fSemiBold, 12), "Start") + 22
			bx := x + w - padX - bw + 2
			key := "btn:edge:start"
			fill := p.green
			if u.hoverKey == key {
				fill = mix(p.green, p.fg, 0.25)
			}
			u.cv.roundRect(bx, cy+4, bw, rowH-8, (rowH-8)/2, alpha(fill, a))
			u.cv.text(face(fSemiBold, 12), "Start", bx+11, cy+rowH/2+4.5, alpha(p.ink, a))
			u.hits = append(u.hits, hit{image.Rect(int(bx), int(cy), int(bx+bw), int(cy+rowH)), key})
			right = ""
			u.cv.text(tf, ellipsize(tf, e.Task, w-2*padX-bw-16), x+padX+6, cy+rowH/2+4.5, alpha(p.fg, a))
		} else {
			right = mins(u.now().Sub(e.Started))
			rw := measure(sf, right)
			u.cv.text(sf, right, x+w-padX-rw-2, cy+rowH/2+4.5, alpha(p.green, a))
			u.cv.text(tf, ellipsize(tf, e.Task, w-2*padX-rw-20), x+padX+6, cy+rowH/2+4.5, alpha(p.fg, a))
		}
		cy += rowH + 4
	}
	ss := u.activeSessions()
	if len(ss) == 0 {
		u.cv.text(sf, "No Claude Code sessions", x+padX+6, cy+16, alpha(p.faint, a))
		return
	}
	for i, s := range ss {
		if i == 4 {
			break
		}
		key := "focus:" + s.ID
		if u.hoverKey == key {
			u.cv.roundRect(x+padX-4, cy, w-2*padX+8, rowH, 10, alpha(p.raised, a))
		}
		dot := p.faint
		switch s.State {
		case "work":
			dot = p.green
		case "wait":
			dot = p.blue
		}
		u.cv.circle(x+padX+6, cy+rowH/2, 3.5, alpha(dot, a))
		pf := face(fSemiBold, 12.5)
		pw := u.cv.text(pf, ellipsize(pf, s.Project, 120), x+padX+18, cy+rowH/2+4.5, alpha(p.fg, a))
		el := mins(u.now().Sub(s.Since))
		ew := measure(sf, el)
		u.cv.text(sf, el, x+w-padX-ew-2, cy+rowH/2+4.5, alpha(p.faint, a))
		lbl := s.Label
		if s.State == "wait" {
			lbl = "waiting for you"
		} else if s.State == "idle" {
			lbl = "idle"
		}
		lf := face(fRegular, 12)
		u.cv.text(lf, ellipsize(lf, lbl, w-2*padX-pw-ew-40), x+padX+18+pw+8, cy+rowH/2+4.5, alpha(p.muted, a))
		u.hits = append(u.hits, hit{image.Rect(int(x+padX-4), int(cy), int(x+w-padX+4), int(cy+rowH)), key})
		cy += rowH
	}
}

// ---- input ----

func (u *UI) hitAt(x, y float64) string {
	for i := len(u.hits) - 1; i >= 0; i-- {
		if (image.Point{int(x), int(y)}).In(u.hits[i].r) {
			return u.hits[i].key
		}
	}
	return ""
}

func (u *UI) pointer(e pointerEvent) (redraw, hand bool) {
	switch e.kind {
	case "enter", "motion":
		u.mx, u.my = e.x, e.y
		if !u.hover {
			u.hover = true
			redraw = true
		}
	case "leave":
		u.hover = false
		u.leftAt = u.now()
		u.hoverKey = ""
		return true, false
	case "press":
		k := u.hitAt(e.x, e.y)
		u.press(k)
		return true, false
	}
	k := u.hitAt(u.mx, u.my)
	if k != u.hoverKey {
		u.hoverKey = k
		redraw = true
	}
	hand = strings.HasPrefix(k, "btn:") || strings.HasPrefix(k, "opt:") || strings.HasPrefix(k, "focus:") || strings.HasPrefix(k, "close:")
	return redraw, hand
}

func (u *UI) press(k string) {
	c := u.top()
	switch {
	case strings.HasPrefix(k, "close:") && c != nil:
		u.RemoveCard(c.ID)
		if c.OnPick != nil {
			c.OnPick("dismiss", nil)
		}
	case strings.HasPrefix(k, "opt:") && c != nil:
		var i int
		fmt.Sscanf(k, "opt:%d", &i)
		if c.Multi {
			if c.picked == nil {
				c.picked = map[int]bool{}
			}
			c.picked[i] = !c.picked[i]
			return
		}
		u.RemoveCard(c.ID)
		if c.OnPick != nil {
			c.OnPick("option", []int{i})
		}
	case strings.HasPrefix(k, "btn:edge:"):
		if u.onKey != nil {
			u.onKey(strings.TrimPrefix(k, "btn:edge:"))
		}
	case strings.HasPrefix(k, "btn:") && c != nil:
		var picked []int
		for i := range c.Options {
			if c.picked[i] {
				picked = append(picked, i)
			}
		}
		u.RemoveCard(c.ID)
		if c.OnPick != nil {
			c.OnPick(strings.TrimPrefix(k, "btn:"), picked)
		}
	case strings.HasPrefix(k, "focus:"):
		if u.onKey != nil {
			u.onKey(k)
		}
	}
}
