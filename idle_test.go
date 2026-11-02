package main

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeCompositor answers the idle watch like niri does: the globals, the
// sync, then whatever events the test sends on the notification.
type fakeCompositor struct {
	conn net.Conn
	r    *bufio.Reader
	reqs chan [3]uint32 // object, opcode, first arg
}

func newFakeCompositor(t *testing.T, notifierVer uint32) (*fakeCompositor, func() (io.ReadWriteCloser, error)) {
	t.Helper()
	srv, cli := net.Pipe()
	f := &fakeCompositor{conn: srv, r: bufio.NewReader(srv), reqs: make(chan [3]uint32, 16)}
	go func() {
		for {
			obj, op, body, err := wlRead(f.r)
			if err != nil {
				close(f.reqs)
				return
			}
			d := wlDec{b: body}
			f.reqs <- [3]uint32{obj, uint32(op), d.u32()}
		}
	}()
	t.Cleanup(func() { srv.Close() })
	return f, func() (io.ReadWriteCloser, error) {
		go func() {
			global := func(name uint32, iface string, ver uint32) {
				b := make([]byte, 8)
				binary.LittleEndian.PutUint32(b, 2)
				b = binary.LittleEndian.AppendUint32(b, name)
				b = wlStr(b, iface)
				b = binary.LittleEndian.AppendUint32(b, ver)
				binary.LittleEndian.PutUint32(b[4:], uint32(len(b))<<16)
				srv.Write(b)
			}
			global(1, "wl_compositor", 6)
			global(7, "wl_seat", 9)
			if notifierVer > 0 {
				global(9, "ext_idle_notifier_v1", notifierVer)
			}
			srv.Write(wlMsg(3, 0, 1)) // callback done
		}()
		return cli, nil
	}
}

func (f *fakeCompositor) event(obj uint32, op uint16) { f.conn.Write(wlMsg(obj, op)) }

// The watch binds the seat and the notifier, asks for input idle (v2) after
// idleAfter, and follows idled and resumed.
func TestIdleWatch(t *testing.T) {
	f, dial := newFakeCompositor(t, 2)
	x := newSensor()
	done := make(chan error, 1)
	go func() { done <- x.idleStream(dial) }()
	want := [][3]uint32{{1, 1, 2}, {1, 0, 3}, {2, 0, 7}, {2, 0, 9}, {5, 2, 6}} // bind seat, bind notifier, get_input_idle_notification
	for _, w := range want {
		select {
		case got := <-f.reqs:
			if got != w {
				t.Fatalf("request %v, want %v", got, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no request")
		}
	}
	wait := func(away bool) {
		t.Helper()
		for i := 0; i < 200; i++ {
			if known, a := x.presence(); known && a == away {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("presence never became away=%v", away)
	}
	wait(false)
	f.event(6, 0) // idled
	wait(true)
	f.event(6, 1) // resumed
	wait(false)
	f.conn.Close()
	if err := <-done; err == nil {
		t.Fatal("the stream ended without an error")
	}
}

func TestIdleWatchWithoutNotifier(t *testing.T) {
	_, dial := newFakeCompositor(t, 0)
	if err := newSensor().idleStream(dial); err != errNoIdle {
		t.Fatalf("err %v, want errNoIdle", err)
	}
}

// Away, a focused workspace terminal is no longer work, and the minutes
// before the idle fired are taken back.
func TestAwayIsNotWork(t *testing.T) {
	x := newSensor()
	mono := time.Unix(1000, 0)
	x.clock = func() time.Time { return mono }
	x.streamUp = true
	x.wins[1] = window{ID: 1, App: "zen", Title: "Codeforces - Problem C"}
	x.focus = 1
	st := senseTask{ID: "cf:1C", Title: "Problem C", Count: true}
	x.take(st, window{})
	x.setPresence(true, false)
	mono = mono.Add(20 * time.Minute)
	x.setPresence(true, true) // idled: no input for the last 5 of those 20
	mono = mono.Add(30 * time.Minute)
	sec, _, on := x.take(st, window{})
	if on || sec != 15*60 {
		t.Fatalf("away: worked %ds on=%v, want 900s off", sec, on)
	}
	x.setPresence(true, false)
	mono = mono.Add(2 * time.Minute)
	if sec, _, on = x.take(st, window{}); !on || sec != 120 {
		t.Fatalf("back: %ds on=%v", sec, on)
	}
}
