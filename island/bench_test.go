package island

import (
	"testing"
	"time"
)

func benchUI(b *testing.B, setup func(*UI)) {
	u := NewUI(420, 240)
	clock := time.Now()
	u.now = func() time.Time { return clock }
	setup(u)
	for i := 0; i < 120; i++ {
		clock = clock.Add(time.Second / 60)
		u.render()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clock = clock.Add(time.Second / 60)
		u.render()
	}
}

func BenchmarkCompact(b *testing.B) {
	benchUI(b, func(u *UI) {
		u.sessions["a"] = &Session{ID: "a", Project: "edge", State: "work", Label: "Editing quota.go", Plus: 12, Minus: 3}
	})
}

func BenchmarkCard(b *testing.B) {
	benchUI(b, func(u *UI) {
		u.AddCard(&Card{ID: "p", Kind: "perm", Mood: moodWait, Title: "Run this command?", Meta: "kinvault", Body: "Run tests",
			Code: "go test -race ./...", Buttons: []Button{{Key: "deny", Label: "Deny"}, {Key: "allow", Label: "Allow", Primary: true}}})
	})
}

func BenchmarkEmberOnly(b *testing.B) {
	u := NewUI(420, 240)
	clock := time.Now()
	u.now = func() time.Time { return clock }
	u.sessions["a"] = &Session{ID: "a", Project: "edge", State: "work", Label: "Editing quota.go"}
	for i := 0; i < 120; i++ {
		clock = clock.Add(time.Second / 60)
		u.render()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clock = clock.Add(time.Second / 15)
		u.render()
	}
}
