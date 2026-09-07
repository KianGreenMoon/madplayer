// Package tray is madplayer's presence in the desktop's notification area
// while its window is closed: a StatusNotifierItem on the session bus, with a
// two-item menu.
//
// It exists for node mode (madshare docs/plans/full-node-mode.md P5):
// membership is presence, and a node that dies when the player window closes
// is exactly the churny neighbour the availability machinery keeps writing
// off. What survives in the tray is the whole process — the mesh, seeding,
// catalog sync, and playback too — and the icon is what says so, and what
// brings the window back.
//
// Hand-rolled over godbus rather than a tray library, for the same reason the
// media bus is (internal/mpris): the bus is already a dependency, the two
// interfaces are small, and a library would bring cgo or its own main loop.
// The interfaces are freedesktop's de-facto ones — org.kde.StatusNotifierItem
// (KDE, waybar, and GNOME's AppIndicator extension) and com.canonical.dbusmenu
// for the menu; the two are what every host that shows a tray at all speaks.
//
// A host may be absent (GNOME without the extension, a bare X session) or
// arrive later (a bar that starts after this program at login). New succeeds
// without one — the bus is the requirement, not the host — and Hosted reports
// whether an icon is actually being shown; the item registers itself whenever
// a watcher appears. The caller must not hide its window behind an icon that
// nobody displays, so it asks Hosted first.
package tray

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"log"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	watcherName  = "org.kde.StatusNotifierWatcher"
	watcherPath  = "/StatusNotifierWatcher"
	watcherIface = "org.kde.StatusNotifierWatcher"

	itemIface = "org.kde.StatusNotifierItem"
	itemPath  = "/StatusNotifierItem"

	menuIface = "com.canonical.dbusmenu"
	menuPath  = "/MenuBar"
)

// Menu item ids. dbusmenu's root is 0; the rest are ours.
const (
	menuRoot int32 = iota
	menuShow
	menuSep
	menuQuit
)

// Options describe the item.
type Options struct {
	// ID is the item's Id property and part of its bus name: the program's
	// name, "madplayer".
	ID string
	// Title is what a host shows on hover or in a list of items.
	Title string
	// IconName names an icon in the desktop's theme (the packaging installs
	// one as "madplayer"); Icons are the pixels for a host that cannot find
	// it — several sizes, the host picks the nearest. Give both.
	IconName string
	Icons    []image.Image
	// ShowLabel and QuitLabel are the menu's two entries.
	ShowLabel, QuitLabel string
	// OnShow is a click on the icon or the first menu entry: bring the window
	// back. OnQuit is the second entry: stop the program, node and all. Both
	// are called on the bus goroutine and must return promptly.
	OnShow, OnQuit func()
	// Log receives the item's one-line reports; nil means the standard logger.
	Log *log.Logger
}

// Item is a registered tray item.
type Item struct {
	conn  *dbus.Conn
	own   bool // whether Close should close conn
	name  string
	opts  Options
	props *prop.Properties
	log   *log.Logger

	mu     sync.Mutex
	hosted bool
	closed bool
	done   chan struct{}
}

// ErrNoBus is New's answer when there is no session bus at all — a machine
// with no desktop, which is a normal machine.
var ErrNoBus = errors.New("no session bus")

// New puts the item on the session bus. It does not need a host to succeed;
// see Hosted.
func New(opts Options) (*Item, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoBus, err)
	}
	// dbus.SessionBus is a shared connection (the media bus is on it too), so
	// it is not ours to close.
	return newOn(conn, false, opts)
}

func newOn(conn *dbus.Conn, own bool, opts Options) (*Item, error) {
	if opts.ID == "" {
		return nil, errors.New("tray: an item needs an ID")
	}
	if opts.Log == nil {
		opts.Log = log.Default()
	}
	it := &Item{conn: conn, own: own, opts: opts, log: opts.Log, done: make(chan struct{})}
	// The name the spec suggests: org.kde.StatusNotifierItem-<pid>-<n>. Hosts
	// resolve the item's object at /StatusNotifierItem on whatever name they
	// are handed, so the well-known name is what gets registered.
	it.name = fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := conn.RequestName(it.name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, fmt.Errorf("tray: request name: %w", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("tray: bus name %s is taken", it.name)
	}
	if err := it.export(); err != nil {
		conn.ReleaseName(it.name)
		return nil, err
	}
	// Register with the watcher now if there is one, and again whenever one
	// appears — a bar restarted, or started after this program.
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherName),
	); err != nil {
		it.log.Printf("tray: cannot watch for a tray host: %v", err)
	} else {
		ch := make(chan *dbus.Signal, 8)
		conn.Signal(ch)
		go it.watch(ch)
	}
	it.register()
	return it, nil
}

// export puts the item and its menu on the bus.
func (it *Item) export() error {
	c := it.conn
	// The item.
	if err := c.Export(itemHandler{it}, itemPath, itemIface); err != nil {
		return fmt.Errorf("tray: export item: %w", err)
	}
	itemProps := map[string]*prop.Prop{
		"Category":            ro("ApplicationStatus"),
		"Id":                  ro(it.opts.ID),
		"Title":               ro(it.opts.Title),
		"Status":              ro("Active"),
		"WindowId":            ro(int32(0)),
		"IconName":            ro(it.opts.IconName),
		"IconPixmap":          ro(pixmaps(it.opts.Icons)),
		"OverlayIconName":     ro(""),
		"OverlayIconPixmap":   ro([]pixmap{}),
		"AttentionIconName":   ro(""),
		"AttentionIconPixmap": ro([]pixmap{}),
		"AttentionMovieName":  ro(""),
		"ToolTip":             ro(toolTip{IconName: it.opts.IconName, Title: it.opts.Title}),
		"ItemIsMenu":          ro(false),
		"Menu":                ro(dbus.ObjectPath(menuPath)),
		"IconThemePath":       ro(""),
	}
	props, err := prop.Export(c, itemPath, prop.Map{itemIface: itemProps})
	if err != nil {
		return fmt.Errorf("tray: export item properties: %w", err)
	}
	it.props = props
	node := &introspect.Node{
		Name: itemPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name:       itemIface,
				Methods:    introspect.Methods(itemHandler{}),
				Properties: props.Introspection(itemIface),
				Signals: []introspect.Signal{
					{Name: "NewTitle"}, {Name: "NewIcon"}, {Name: "NewAttentionIcon"},
					{Name: "NewOverlayIcon"}, {Name: "NewToolTip"},
					{Name: "NewStatus", Args: []introspect.Arg{{Name: "status", Type: "s"}}},
				},
			},
		},
	}
	if err := c.Export(introspect.NewIntrospectable(node), itemPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return fmt.Errorf("tray: export item introspection: %w", err)
	}

	// The menu.
	m := menuHandler{it}
	if err := c.Export(m, menuPath, menuIface); err != nil {
		return fmt.Errorf("tray: export menu: %w", err)
	}
	menuProps := map[string]*prop.Prop{
		"Version":       ro(uint32(3)),
		"TextDirection": ro("ltr"),
		"Status":        ro("normal"),
		"IconThemePath": ro([]string{}),
	}
	mprops, err := prop.Export(c, menuPath, prop.Map{menuIface: menuProps})
	if err != nil {
		return fmt.Errorf("tray: export menu properties: %w", err)
	}
	mnode := &introspect.Node{
		Name: menuPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name:       menuIface,
				Methods:    introspect.Methods(menuHandler{}),
				Properties: mprops.Introspection(menuIface),
				Signals: []introspect.Signal{
					{Name: "ItemsPropertiesUpdated", Args: []introspect.Arg{
						{Name: "updatedProps", Type: "a(ia{sv})"}, {Name: "removedProps", Type: "a(ias)"}}},
					{Name: "LayoutUpdated", Args: []introspect.Arg{
						{Name: "revision", Type: "u"}, {Name: "parent", Type: "i"}}},
					{Name: "ItemActivationRequested", Args: []introspect.Arg{
						{Name: "id", Type: "i"}, {Name: "timestamp", Type: "u"}}},
				},
			},
		},
	}
	if err := c.Export(introspect.NewIntrospectable(mnode), menuPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return fmt.Errorf("tray: export menu introspection: %w", err)
	}
	return nil
}

// register hands the item to the watcher, if one owns the name right now.
func (it *Item) register() {
	var owner string
	if err := it.conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, watcherName).Store(&owner); err != nil {
		it.setHosted(false)
		return
	}
	call := it.conn.Object(watcherName, watcherPath).Call(watcherIface+".RegisterStatusNotifierItem", 0, it.name)
	if call.Err != nil {
		it.log.Printf("tray: the tray host refused this item: %v", call.Err)
		it.setHosted(false)
		return
	}
	it.setHosted(true)
}

// watch re-registers whenever the watcher name changes hands.
func (it *Item) watch(ch chan *dbus.Signal) {
	for {
		select {
		case <-it.done:
			return
		case sig, ok := <-ch:
			if !ok {
				return
			}
			if sig == nil || sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) != 3 {
				continue
			}
			name, _ := sig.Body[0].(string)
			newOwner, _ := sig.Body[2].(string)
			if name != watcherName {
				continue
			}
			if newOwner == "" {
				it.setHosted(false)
				continue
			}
			it.register()
		}
	}
}

func (it *Item) setHosted(on bool) {
	it.mu.Lock()
	was := it.hosted
	it.hosted = on
	it.mu.Unlock()
	if was != on {
		if on {
			it.log.Printf("tray: icon shown by the desktop's tray host")
		} else {
			it.log.Printf("tray: no tray host on this desktop — closing the window quits")
		}
	}
}

// Hosted reports whether a tray host currently shows the item. The caller
// must ask before hiding a window behind it.
func (it *Item) Hosted() bool {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.hosted
}

// Name is the item's bus name — what a watcher was handed.
func (it *Item) Name() string { return it.name }

// SetToolTip changes what a hover says.
func (it *Item) SetToolTip(title, text string) {
	if it.props == nil {
		return
	}
	it.props.SetMust(itemIface, "ToolTip", toolTip{IconName: it.opts.IconName, Title: title, Text: text})
	it.conn.Emit(itemPath, itemIface+".NewToolTip")
}

// Close takes the item off the bus.
func (it *Item) Close() {
	it.mu.Lock()
	if it.closed {
		it.mu.Unlock()
		return
	}
	it.closed = true
	close(it.done)
	it.mu.Unlock()
	it.conn.ReleaseName(it.name)
	if it.own {
		it.conn.Close()
	}
}

// ── The item interface ───────────────────────────────────────────────────────

type itemHandler struct{ it *Item }

// Activate is a left click: bring the window back.
func (h itemHandler) Activate(x, y int32) *dbus.Error {
	if h.it != nil && h.it.opts.OnShow != nil {
		h.it.opts.OnShow()
	}
	return nil
}

// SecondaryActivate is a middle click; the same act, there is nothing more
// useful to hang on it.
func (h itemHandler) SecondaryActivate(x, y int32) *dbus.Error {
	return h.Activate(x, y)
}

// ContextMenu is a right click on a host that does not do dbusmenu itself.
// The menu has two entries and the first is Activate, so this is that.
func (h itemHandler) ContextMenu(x, y int32) *dbus.Error {
	return h.Activate(x, y)
}

// Scroll is the wheel over the icon; nothing here scrolls.
func (h itemHandler) Scroll(delta int32, orientation string) *dbus.Error { return nil }

// ── The menu interface ───────────────────────────────────────────────────────

type menuHandler struct{ it *Item }

// layoutItem is dbusmenu's (ia{sv}av): id, properties, children (each a
// variant holding another layoutItem).
type layoutItem struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

// idProps is one row of GetGroupProperties: (ia{sv}).
type idProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

func (h menuHandler) items() []idProps {
	show, quit := "Show", "Quit"
	if h.it != nil {
		if h.it.opts.ShowLabel != "" {
			show = h.it.opts.ShowLabel
		}
		if h.it.opts.QuitLabel != "" {
			quit = h.it.opts.QuitLabel
		}
	}
	return []idProps{
		{menuShow, map[string]dbus.Variant{
			"label": dbus.MakeVariant(show), "enabled": dbus.MakeVariant(true), "visible": dbus.MakeVariant(true)}},
		{menuSep, map[string]dbus.Variant{"type": dbus.MakeVariant("separator"), "visible": dbus.MakeVariant(true)}},
		{menuQuit, map[string]dbus.Variant{
			"label": dbus.MakeVariant(quit), "enabled": dbus.MakeVariant(true), "visible": dbus.MakeVariant(true)}},
	}
}

// GetLayout is the whole menu: a root whose children are the two entries.
func (h menuHandler) GetLayout(parent int32, depth int32, names []string) (uint32, layoutItem, *dbus.Error) {
	root := layoutItem{ID: menuRoot, Props: map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")}}
	if parent == menuRoot && depth != 0 {
		for _, e := range h.items() {
			root.Children = append(root.Children, dbus.MakeVariant(layoutItem{ID: e.ID, Props: e.Props, Children: []dbus.Variant{}}))
		}
	} else if parent != menuRoot {
		for _, e := range h.items() {
			if e.ID == parent {
				return 1, layoutItem{ID: e.ID, Props: e.Props, Children: []dbus.Variant{}}, nil
			}
		}
		return 1, layoutItem{}, dbus.MakeFailedError(fmt.Errorf("no menu item %d", parent))
	}
	return 1, root, nil
}

func (h menuHandler) GetGroupProperties(ids []int32, names []string) ([]idProps, *dbus.Error) {
	all := h.items()
	if len(ids) == 0 {
		return all, nil
	}
	var out []idProps
	for _, id := range ids {
		for _, e := range all {
			if e.ID == id {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func (h menuHandler) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	for _, e := range h.items() {
		if e.ID == id {
			if v, ok := e.Props[name]; ok {
				return v, nil
			}
		}
	}
	return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("no property %s on item %d", name, id))
}

// Event is a click on an entry.
func (h menuHandler) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID != "clicked" || h.it == nil {
		return nil
	}
	switch id {
	case menuShow:
		if h.it.opts.OnShow != nil {
			h.it.opts.OnShow()
		}
	case menuQuit:
		if h.it.opts.OnQuit != nil {
			h.it.opts.OnQuit()
		}
	}
	return nil
}

// eventGroupEntry is one of EventGroup's (isvu).
type eventGroupEntry struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

func (h menuHandler) EventGroup(events []eventGroupEntry) ([]int32, *dbus.Error) {
	var unknown []int32
	for _, e := range events {
		if e.ID != menuShow && e.ID != menuQuit && e.ID != menuSep {
			unknown = append(unknown, e.ID)
			continue
		}
		h.Event(e.ID, e.EventID, e.Data, e.Timestamp)
	}
	if unknown == nil {
		unknown = []int32{}
	}
	return unknown, nil
}

// AboutToShow: the menu never changes, so nothing needs updating.
func (h menuHandler) AboutToShow(id int32) (bool, *dbus.Error) { return false, nil }

func (h menuHandler) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}

// ── Wire types ───────────────────────────────────────────────────────────────

// pixmap is the item's (iiay): width, height, ARGB32 in network byte order.
type pixmap struct {
	W, H int32
	Data []byte
}

// toolTip is (sa(iiay)ss).
type toolTip struct {
	IconName string
	Pixmaps  []pixmap
	Title    string
	Text     string
}

func pixmaps(imgs []image.Image) []pixmap {
	out := make([]pixmap, 0, len(imgs))
	for _, img := range imgs {
		if img == nil {
			continue
		}
		out = append(out, toPixmap(img))
	}
	return out
}

// toPixmap converts any image to the spec's ARGB32 big-endian bytes.
func toPixmap(img image.Image) pixmap {
	b := img.Bounds()
	rgba := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	data := make([]byte, 0, 4*b.Dx()*b.Dy())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := rgba.NRGBAAt(x, y)
			data = append(data, c.A, c.R, c.G, c.B)
		}
	}
	return pixmap{W: int32(b.Dx()), H: int32(b.Dy()), Data: data}
}

func ro(v any) *prop.Prop {
	return &prop.Prop{Value: v, Writable: false, Emit: prop.EmitTrue}
}
