package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Presence comes from the compositor: ext-idle-notify-v1 says when there's
// been no keyboard or pointer input for idleAfter, and when input is back.
// Without it, a terminal in the workspace or a judge tab left focused counts
// as work while he's away, and a locked screen whose window titles change
// looks like someone at the laptop. Version 2's input idle ignores idle
// inhibitors, so a playing video doesn't count as him.

// idleAfter: this long without input is away. Reading and thinking scroll or
// type within it; the work it covered is taken back when it fires.
const idleAfter = 5 * time.Minute

var errNoIdle = errors.New("the compositor has no ext_idle_notifier_v1")

// runIdle keeps the idle watch up, reconnecting with backoff. While it's
// down, presence is unknown and the sensor works as before.
func (x *sensor) runIdle() {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	fails := 0
	for {
		began := time.Now()
		err := x.idleStream(wlDial)
		x.setPresence(false, false)
		if errors.Is(err, errNoIdle) {
			log.Println("idle watch:", err)
			return
		}
		if time.Since(began) > 5*time.Minute {
			fails = 0
		}
		fails++
		wait := min(5*time.Minute, time.Second<<min(fails-1, 8))
		log.Printf("idle watch ended (%v); restarting in %s", err, wait)
		time.Sleep(wait)
	}
}

func wlDial() (io.ReadWriteCloser, error) {
	name := os.Getenv("WAYLAND_DISPLAY")
	if !filepath.IsAbs(name) {
		name = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), name)
	}
	return net.Dial("unix", name)
}

// idleStream binds the seat and the idle notifier, asks for one idle
// notification and follows it until the connection ends.
func (x *sensor) idleStream(dial func() (io.ReadWriteCloser, error)) error {
	c, err := dial()
	if err != nil {
		return err
	}
	defer c.Close()
	r := bufio.NewReader(c)
	const (
		display  = 1
		registry = 2
		syncCB   = 3
		seat     = 4
		notifier = 5
		note     = 6
	)
	if _, err := c.Write(append(wlMsg(display, 1, registry), wlMsg(display, 0, syncCB)...)); err != nil { // get_registry, sync
		return err
	}
	var seatName, idleName, idleVer uint32
	for synced := false; !synced; {
		obj, op, body, err := wlRead(r)
		if err != nil {
			return err
		}
		switch {
		case obj == display && op == 0:
			return wlError(body)
		case obj == registry && op == 0: // global: name, interface, version
			d := wlDec{b: body}
			name, iface, ver := d.u32(), d.str(), d.u32()
			switch iface {
			case "wl_seat":
				if seatName == 0 {
					seatName = name
				}
			case "ext_idle_notifier_v1":
				idleName, idleVer = name, min(ver, 2)
			}
		case obj == syncCB && op == 0: // done
			synced = true
		}
	}
	if idleName == 0 || seatName == 0 {
		return errNoIdle
	}
	op := uint16(1) // get_idle_notification
	if idleVer >= 2 {
		op = 2 // get_input_idle_notification
	}
	req := wlBind(registry, seatName, "wl_seat", 1, seat)
	req = append(req, wlBind(registry, idleName, "ext_idle_notifier_v1", idleVer, notifier)...)
	req = append(req, wlMsg(notifier, op, note, uint32(idleAfter.Milliseconds()), seat)...)
	if _, err := c.Write(req); err != nil {
		return err
	}
	x.setPresence(true, false)
	for {
		obj, op, body, err := wlRead(r)
		if err != nil {
			return err
		}
		switch {
		case obj == display && op == 0:
			return wlError(body)
		case obj == note:
			x.setPresence(true, op == 0) // 0 idled, 1 resumed
		}
	}
}

// ---- the Wayland wire, just enough for the idle watch ----

// wlMsg is one request whose arguments are all uint32s.
func wlMsg(obj uint32, op uint16, args ...uint32) []byte {
	b := make([]byte, 8, 8+4*len(args))
	binary.LittleEndian.PutUint32(b, obj)
	for _, a := range args {
		b = binary.LittleEndian.AppendUint32(b, a)
	}
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b))<<16|uint32(op))
	return b
}

// wlBind is wl_registry.bind with its untyped new_id: name, interface, version, id.
func wlBind(registry, name uint32, iface string, ver, id uint32) []byte {
	b := make([]byte, 8, 64)
	binary.LittleEndian.PutUint32(b, registry)
	b = binary.LittleEndian.AppendUint32(b, name)
	b = wlStr(b, iface)
	b = binary.LittleEndian.AppendUint32(b, ver)
	b = binary.LittleEndian.AppendUint32(b, id)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b))<<16) // opcode 0
	return b
}

func wlStr(b []byte, s string) []byte {
	n := len(s) + 1
	b = binary.LittleEndian.AppendUint32(b, uint32(n))
	b = append(b, s...)
	return append(b, make([]byte, 1+(4-n%4)%4)...) // NUL, then pad to 4
}

// wlRead reads one event: object, opcode and its argument bytes.
func wlRead(r io.Reader) (obj uint32, op uint16, body []byte, err error) {
	var h [8]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return
	}
	obj = binary.LittleEndian.Uint32(h[:])
	w := binary.LittleEndian.Uint32(h[4:])
	op, size := uint16(w&0xffff), int(w>>16)
	if size < 8 {
		return 0, 0, nil, fmt.Errorf("wayland: bad message size %d", size)
	}
	body = make([]byte, size-8)
	_, err = io.ReadFull(r, body)
	return
}

func wlError(body []byte) error {
	d := wlDec{b: body}
	obj, code, msg := d.u32(), d.u32(), d.str()
	return fmt.Errorf("wayland error on object %d (code %d): %s", obj, code, msg)
}

type wlDec struct{ b []byte }

func (d *wlDec) u32() uint32 {
	if len(d.b) < 4 {
		return 0
	}
	v := binary.LittleEndian.Uint32(d.b)
	d.b = d.b[4:]
	return v
}

func (d *wlDec) str() string {
	n := int(d.u32())
	if n == 0 || n > len(d.b) {
		return ""
	}
	s := string(d.b[:n-1])
	d.b = d.b[min(len(d.b), (n+3)&^3):]
	return s
}
