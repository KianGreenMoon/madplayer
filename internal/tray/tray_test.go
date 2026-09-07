package tray

import (
	"bufio"
	"context"
	"image"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// privateBus starts a dbus-daemon of the test's own — the session bus is the
// desktop's, and an item registered there flashes in the person's tray on
// every run. Skips where there is no daemon to start.
func privateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("no dbus-daemon on this host")
	}
	sock := filepath.Join(t.TempDir(), "bus")
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "dbus-daemon", "--session", "--nofork", "--nopidfile",
		"--print-address=1", "--address=unix:path="+sock)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("dbus-daemon printed no address: %v", err)
	}
	return strings.TrimSpace(line)
}

func connect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	c, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// watcher is a fake StatusNotifierWatcher: it owns the name and records what
// registers with it.
type watcher struct {
	conn       *dbus.Conn
	registered chan string
}

func startWatcher(t *testing.T, addr string) *watcher {
	t.Helper()
	w := &watcher{conn: connect(t, addr), registered: make(chan string, 8)}
	if err := w.conn.Export(w, dbus.ObjectPath(watcherPath), watcherIface); err != nil {
		t.Fatal(err)
	}
	w.claim(t)
	return w
}

func (w *watcher) claim(t *testing.T) {
	t.Helper()
	reply, err := w.conn.RequestName(watcherName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("claim the watcher name: %v (%v)", err, reply)
	}
}

func (w *watcher) RegisterStatusNotifierItem(service string) *dbus.Error {
	w.registered <- service
	return nil
}

func waitHosted(t *testing.T, it *Item, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if it.Hosted() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Hosted() never became %v", want)
}

func waitRegistered(t *testing.T, w *watcher, name string) {
	t.Helper()
	select {
	case got := <-w.registered:
		if got != name {
			t.Fatalf("registered %q, want %q", got, name)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the item never registered with the watcher")
	}
}

func newItem(t *testing.T, addr string, onShow, onQuit func()) *Item {
	t.Helper()
	it, err := newOn(connect(t, addr), false, Options{
		ID: "madplayer", Title: "madplayer", IconName: "madplayer",
		Icons:     []image.Image{image.NewNRGBA(image.Rect(0, 0, 2, 2))},
		ShowLabel: "Show madplayer", QuitLabel: "Quit madplayer",
		OnShow: onShow, OnQuit: onQuit, Log: discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(it.Close)
	return it
}

func TestAnItemRegistersWithTheWatcherAndFollowsItsRestarts(t *testing.T) {
	addr := privateBus(t)
	w := startWatcher(t, addr)
	it := newItem(t, addr, nil, nil)

	waitRegistered(t, w, it.Name())
	waitHosted(t, it, true)

	// The bar restarts: the name goes away, then comes back.
	if _, err := w.conn.ReleaseName(watcherName); err != nil {
		t.Fatal(err)
	}
	waitHosted(t, it, false)
	w.claim(t)
	waitRegistered(t, w, it.Name())
	waitHosted(t, it, true)
}

func TestAnItemWithoutAHostWaitsForOne(t *testing.T) {
	addr := privateBus(t)
	it := newItem(t, addr, nil, nil)
	if it.Hosted() {
		t.Fatalf("hosted with no watcher on the bus")
	}
	w := startWatcher(t, addr)
	waitRegistered(t, w, it.Name())
	waitHosted(t, it, true)
}

func TestTheMenuAndTheClicksReachTheProgram(t *testing.T) {
	addr := privateBus(t)
	w := startWatcher(t, addr)
	shown, quit := make(chan struct{}, 4), make(chan struct{}, 4)
	it := newItem(t, addr, func() { shown <- struct{}{} }, func() { quit <- struct{}{} })
	waitRegistered(t, w, it.Name())

	// The host reads the menu the way a bar does.
	obj := w.conn.Object(it.Name(), dbus.ObjectPath(menuPath))
	var rev uint32
	var root layoutItem
	if err := obj.Call(menuIface+".GetLayout", 0, int32(0), int32(-1), []string{}).Store(&rev, &root); err != nil {
		t.Fatalf("GetLayout: %v", err)
	}
	if len(root.Children) != 3 {
		t.Fatalf("menu has %d entries, want show · separator · quit", len(root.Children))
	}
	var labels []string
	for _, c := range root.Children {
		var e layoutItem
		if err := dbus.Store([]any{c.Value()}, &e); err != nil {
			t.Fatalf("child: %v", err)
		}
		if l, ok := e.Props["label"]; ok {
			labels = append(labels, l.Value().(string))
		}
	}
	if strings.Join(labels, "|") != "Show madplayer|Quit madplayer" {
		t.Fatalf("labels %v", labels)
	}

	// A click on the icon, and on each entry.
	item := w.conn.Object(it.Name(), dbus.ObjectPath(itemPath))
	if err := item.Call(itemIface+".Activate", 0, int32(0), int32(0)).Err; err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := obj.Call(menuIface+".Event", 0, menuShow, "clicked", dbus.MakeVariant(0), uint32(0)).Err; err != nil {
		t.Fatalf("Event show: %v", err)
	}
	if err := obj.Call(menuIface+".Event", 0, menuQuit, "clicked", dbus.MakeVariant(0), uint32(0)).Err; err != nil {
		t.Fatalf("Event quit: %v", err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-shown:
		case <-time.After(2 * time.Second):
			t.Fatalf("show #%d never reached the program", i+1)
		}
	}
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatalf("quit never reached the program")
	}

	// The properties a host reads before drawing.
	var pm []pixmap
	if err := item.Call("org.freedesktop.DBus.Properties.Get", 0, itemIface, "IconPixmap").Store(&pm); err != nil {
		t.Fatalf("IconPixmap: %v", err)
	}
	if len(pm) != 1 || pm[0].W != 2 || pm[0].H != 2 || len(pm[0].Data) != 16 {
		t.Fatalf("pixmap %+v", pm)
	}
	var status string
	if err := item.Call("org.freedesktop.DBus.Properties.Get", 0, itemIface, "Status").Store(&status); err != nil || status != "Active" {
		t.Fatalf("Status %q, %v", status, err)
	}
	it.SetToolTip("madplayer", "node running")
	var tip toolTip
	if err := item.Call("org.freedesktop.DBus.Properties.Get", 0, itemIface, "ToolTip").Store(&tip); err != nil || tip.Text != "node running" {
		t.Fatalf("ToolTip %+v, %v", tip, err)
	}
}

func TestPixmapIsARGBNetworkOrder(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3] = 0x11, 0x22, 0x33, 0xff
	p := toPixmap(img)
	if p.W != 1 || p.H != 1 || string(p.Data) != string([]byte{0xff, 0x11, 0x22, 0x33}) {
		t.Fatalf("%+v", p)
	}
}

func discard() *log.Logger { return log.New(io.Discard, "", 0) }
