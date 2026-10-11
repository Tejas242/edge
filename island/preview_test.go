package island

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	xdraw "golang.org/x/image/draw"
)

// EDGE_PREVIEW=dir go test -run Preview ./island renders each state to PNG.
func TestPreview(t *testing.T) {
	dir := os.Getenv("EDGE_PREVIEW")
	if dir == "" {
		t.Skip("set EDGE_PREVIEW")
	}
	clock := time.Date(2026, 10, 11, 21, 30, 0, 0, time.Local)
	mk := func() *UI {
		u := NewUI(420, 240)
		u.now = func() time.Time { return clock }
		u.start, u.lastBlink, u.bounceAt = clock, clock, clock.Add(-time.Hour)
		return u
	}
	settle := func(u *UI) {
		for i := 0; i < 120; i++ {
			clock = clock.Add(time.Second / 60)
			u.render()
		}
	}
	save := func(name string, u *UI) {
		settle(u)
		bg := image.NewRGBA(image.Rect(0, 0, 420, 240+28))
		draw.Draw(bg, bg.Rect, image.NewUniform(color.NRGBA{0x2a, 0x33, 0x2b, 255}), image.Point{}, draw.Src)
		draw.Draw(bg, image.Rect(0, 0, 420, 28), image.NewUniform(color.NRGBA{0x0f, 0x15, 0x0e, 255}), image.Point{}, draw.Src)
		draw.Draw(bg, image.Rect(0, 28, 420, 268), u.cv.img, image.Point{}, draw.Over)
		big := image.NewRGBA(image.Rect(0, 0, 840, 536))
		xdraw.CatmullRom.Scale(big, big.Rect, bg, bg.Rect, xdraw.Src, nil)
		f, _ := os.Create(filepath.Join(dir, name+".png"))
		png.Encode(f, big)
		f.Close()
	}

	u := mk()
	save("1-pip", u)

	sheet := newCanvas(420, 90)
	draw.Draw(sheet.img, sheet.img.Rect, image.NewUniform(color.NRGBA{0x08, 0x0c, 0x08, 255}), image.Point{}, draw.Src)
	for i, m := range []mood{moodIdle, moodWork, moodWait, moodHappy, moodError, moodNudge} {
		sheet.ember(u.pal, float32(40+i*68), 48, 56, m, 0.3, 0, 0)
	}
	f, _ := os.Create(filepath.Join(dir, "0-ember.png"))
	png.Encode(f, sheet.img)
	f.Close()

	u = mk()
	u.sessions["a"] = &Session{ID: "a", Project: "edge", State: "work", Label: "Editing quota.go", Plus: 12, Minus: 3, Since: clock.Add(-4 * time.Minute)}
	u.edge = EdgeStatus{Today: 0, Need: 1, Task: "1360E · Polygon", Started: clock.Add(-23 * time.Minute)}
	save("2-compact", u)

	u = mk()
	u.edge = EdgeStatus{Today: 0, Need: 1, Closed: true}
	save("3-compact-locked", u)

	u = mk()
	u.AddCard(&Card{ID: "p", Kind: "perm", Mood: moodWait, Title: "Run this command?", Meta: "kinvault",
		Body: "Run the store tests with the race detector", Code: "go test -race ./store/... -run TestRepair -count=1",
		Buttons: []Button{{Key: "deny", Label: "Deny", Danger: true}, {Key: "always", Label: "Always"}, {Key: "allow", Label: "Allow", Primary: true}},
		Footer:  "In terminal"})
	save("4-permission", u)

	u = mk()
	u.AddCard(&Card{ID: "q", Kind: "ask", Mood: moodWait, Title: "Which storage engine?", Meta: "kinvault · 1/2",
		Options: []Option{{"Pebble", "LSM, battle tested at Cockroach"}, {"bbolt", "B+tree, single file"}, {"Roll our own", "Append-only log + index"}},
		Footer:  "In terminal"})
	save("5-question", u)

	u = mk()
	u.AddCard(&Card{ID: "e", Kind: "edge", Mood: moodWork, Title: "Next problem is ready", Meta: "rating 1300",
		Body:    "1360E · Polygon\nToday 0/1 · streak 3",
		Buttons: []Button{{Key: "snooze", Label: "In 30 min"}, {Key: "start", Label: "Start", Primary: true}}})
	save("6-edge-offer", u)

	u = mk()
	u.AddCard(&Card{ID: "h", Kind: "edge", Mood: moodNudge, Title: "20m in · hint 1 unlocked", Meta: "1360E",
		Buttons: []Button{{Key: "keep", Label: "Keep going"}, {Key: "hint", Label: "Get hint", Primary: true}}})
	save("7-hint", u)

	u = mk()
	u.sessions["a"] = &Session{ID: "a", Project: "edge", State: "work", Label: "Running go test ./...", Since: clock.Add(-4 * time.Minute)}
	u.sessions["b"] = &Session{ID: "b", Project: "kinvault", State: "wait", Since: clock.Add(-31 * time.Minute)}
	u.sessions["c"] = &Session{ID: "c", Project: "CP", State: "idle", Since: clock.Add(-2 * time.Hour)}
	u.edge = EdgeStatus{Today: 0, Need: 1, Streak: 3, Closed: true, Task: "1360E · Polygon", Pending: true}
	u.hover = true
	save("8-panel", u)

	u = mk()
	u.AddCard(&Card{ID: "d", Kind: "done", Mood: moodHappy, Title: "Accepted · 34m", Meta: "1360E",
		Body:    "Topic: geometry. Today 1/1, so Claude is unlocked.",
		Buttons: []Button{{Key: "explain", Label: "Explain-back (2 min)", Primary: true}}})
	save("9-accepted", u)
}
