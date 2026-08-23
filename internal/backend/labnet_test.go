package backend

// The madnetwork lab: a complete, throwaway madnetwork built inside one test
// process, so the swarm path can be exercised without touching anybody's real
// network. It exists because the failures people actually see — "didn't verify
// this track", tracks skipping, a track that plays its first seconds and dies —
// are about STANDING between nodes, and standing cannot be faked at the unit
// level: the refusal happens inside the holder's own verifier.
//
// The shape is the household (madshare docs/architecture/federation-access.md
// §"The household"), built from real parts:
//
//	hub      a transport-only yggdrasil node listening on loopback — the lab's
//	         stand-in for "the network between the machines". Both devices peer
//	         to it and route to each other through it. Multicast is OFF on
//	         every node (Options.NoMulticast), because a developer machine may
//	         run a real yggdrasil daemon and the lab must never join it.
//	home     an httptest server playing the home server's four mesh endpoints
//	         with a REAL ed25519 identity: the tokens it issues are signed with
//	         federation.SignCapabilityToken and verified by the real code on
//	         the holder. It holds no music — like a real home server whose
//	         madnetwork rows name other people's blobs.
//	seeder   a real madplayer backend (embedded madshare, real mesh node) whose
//	         download cache holds the track. It serves exactly what its home
//	         server vouches for, through the real audience ladder.
//	player   a second real madplayer backend, fetching through the real
//	         enrolment loop and the real remote.Fetcher — everything
//	         cmd/madplayer does except paint.
//
// Each test builds its own network. Nothing here reaches beyond 127.0.0.1.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	mrand "math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"daemonlord.ygg/madshare/app"
	"daemonlord.ygg/madshare/config"
	"daemonlord.ygg/madshare/federation"

	"daemonlord.ygg/madplayer/internal/blobcache"
	"daemonlord.ygg/madplayer/internal/library"
	madclient "daemonlord.ygg/madplayer/internal/madshare"
	"daemonlord.ygg/madplayer/internal/mesh"
	"daemonlord.ygg/madplayer/internal/queue"
	"daemonlord.ygg/madplayer/internal/remote"
)

// labLogger writes to stderr under -v and nowhere otherwise. Deliberately not
// t.Logf: the backends keep goroutines alive into cleanup, and a log line
// after the test function returns would panic the harness.
func labLogger(name string) *log.Logger {
	if !testing.Verbose() {
		return log.New(io.Discard, "", 0)
	}
	return log.New(os.Stderr, "lab["+name+"] ", log.Ltime|log.Lmicroseconds)
}

// reservePort picks a free loopback TCP port, reserve-then-close — the same
// pattern madshare's own mesh tests use, and the same reason these tests do
// not run in parallel.
func reservePort(t *testing.T) string {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	return addr
}

// startLabHub starts the underlay both devices meet on: a transport-only
// yggdrasil node with one loopback listener, no multicast, no protocol on top.
func startLabHub(t *testing.T) (uri string) {
	t.Helper()
	addr := reservePort(t)
	m, err := federation.StartTransport(config.YggdrasilConfig{
		KeyFile: filepath.Join(t.TempDir(), "hub.key"),
		Listen:  []string{"tcp://" + addr},
	}, labLogger("hub"))
	if err != nil {
		t.Fatalf("start lab hub: %v", err)
	}
	t.Cleanup(m.Stop)
	return "tcp://" + addr
}

// labHolder is one entry of a lab fetch plan.
type labHolder struct {
	key  string
	size int64
}

// labHome plays a home server's mesh role over HTTP: it vouches (with a real
// signature), points at the underlay, accepts advertisements, and names
// holders. What it does NOT do is hold bytes — the madnetwork rows a real
// server hands a player name blobs the server itself never stored, which is
// exactly why these failures have no relay to hide behind.
type labHome struct {
	t    *testing.T
	priv ed25519.PrivateKey
	key  string // this server's node identity, lowercase hex — the token issuer
	srv  *httptest.Server
	hub  string

	mu      sync.Mutex
	stale   bool
	holders map[string][]labHolder
}

func newLabHome(t *testing.T, hub string) *labHome {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &labHome{t: t, priv: priv, key: hex.EncodeToString(pub), hub: hub, holders: map[string][]labHolder{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/madnetwork/token", h.token)
	mux.HandleFunc("/api/madnetwork/peering", h.peering)
	mux.HandleFunc("/api/madnetwork/holdings", h.holdings)
	mux.HandleFunc("/api/madnetwork/holders/", h.plan)
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *labHome) URL() string { return h.srv.URL }

// setStale makes every token this server issues from now on ALREADY EXPIRED —
// signed ninety minutes in the past, dead for thirty, well beyond every
// verifier's clock slack. That is not a server anybody would run; it is the
// state a DEVICE wakes into after sleeping through its token's whole life: the
// monotonic clocks pacing the enrolment loop stood still with the machine, so
// no renewal is due, while the wall clock every verifier reads moved on.
func (h *labHome) setStale(v bool) {
	h.mu.Lock()
	h.stale = v
	h.mu.Unlock()
}

// offer adds a holder to this server's fetch plan for one hash.
func (h *labHome) offer(hash, holderKey string, size int64) {
	h.mu.Lock()
	h.holders[hash] = append(h.holders[hash], labHolder{key: holderKey, size: size})
	h.mu.Unlock()
}

func (h *labHome) token(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeKey string `json:"node_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeKey == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	stale := h.stale
	h.mu.Unlock()
	issued := time.Now()
	if stale {
		issued = issued.Add(-90 * time.Minute)
	}
	tok, expires, err := federation.SignCapabilityToken(h.priv, h.key, req.NodeKey, false, issued)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(madclient.Grant{
		Token:  tok,
		Issuer: h.key,
		Bearer: req.NodeKey,
		// The dates a client is TOLD, and they matter: RenewAfter far in the
		// future is what an enrolment believes after a long sleep — "not due
		// yet" — however dead the token underneath it is.
		ExpiresAt:  expires,
		RenewAfter: time.Now().Add(30 * time.Minute),
	})
}

func (h *labHome) peering(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"peers": []string{h.hub}, "listen": []string{}})
}

func (h *labHome) holdings(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"refresh_after": 3600})
}

func (h *labHome) plan(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/api/madnetwork/holders/")
	h.mu.Lock()
	list := append([]labHolder(nil), h.holders[hash]...)
	h.mu.Unlock()
	type holder struct {
		Key      string `json:"key"`
		Name     string `json:"name"`
		LastSeen int64  `json:"last_seen"`
	}
	out := struct {
		Hash    string   `json:"hash"`
		Size    int64    `json:"size"`
		Holders []holder `json:"holders"`
	}{Hash: hash}
	for _, x := range list {
		out.Size = x.size
		out.Holders = append(out.Holders, holder{Key: x.key, Name: "lab holder", LastSeen: time.Now().Unix()})
	}
	_ = json.NewEncoder(w).Encode(out)
}

// labDevice is one real madplayer: the embedded backend with its mesh node up,
// plus whatever enrolment currently speaks for it.
type labDevice struct {
	t      *testing.T
	name   string
	dir    string
	be     *Backend
	node   app.Network
	enrol  *mesh.Enrolment
	cancel context.CancelFunc
}

func startLabDevice(t *testing.T, name, hub string) *labDevice {
	t.Helper()
	dir := t.TempDir()
	be, err := Open(context.Background(), dir, labLogger(name), Options{
		Mesh:        true,
		Peers:       []string{hub},
		NoMulticast: true,
	})
	if err != nil {
		t.Fatalf("open %s backend: %v", name, err)
	}
	t.Cleanup(be.Close)
	node, up := be.Mesh()
	if !up {
		t.Fatalf("%s: mesh did not come up: %s", name, be.MeshProblem())
	}
	return &labDevice{t: t, name: name, dir: dir, be: be, node: node}
}

// enrolAt runs a fresh enrolment loop against the given homes and waits until
// every round has succeeded once. Calling it again replaces the previous loop,
// which is how a test re-enrols after changing what a home server issues.
func (d *labDevice) enrolAt(homes ...*labHome) *mesh.Enrolment {
	d.t.Helper()
	if d.cancel != nil {
		d.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.t.Cleanup(cancel)

	e := mesh.New(d.node, labLogger(d.name+"/mesh"))
	go e.Run(ctx)
	servers := make([]mesh.Server, 0, len(homes))
	for _, h := range homes {
		servers = append(servers, mesh.Server{Base: h.URL(), Label: "home", Client: madclient.New(h.URL(), "lab-token")})
	}
	e.SetServers(ctx, servers)

	waitUntil(d.t, 30*time.Second, d.name+" enrols", func() (bool, string) {
		byBase := map[string]mesh.Status{}
		for _, st := range e.Status() {
			byBase[st.Base] = st
		}
		for _, h := range homes {
			st, ok := byBase[h.URL()]
			if !ok {
				return false, "no status yet"
			}
			if st.Enrolled.IsZero() {
				return false, st.Problem
			}
		}
		return true, ""
	})
	d.enrol = e
	return e
}

// seed puts a blob into this device's madnetwork cache — the directory its
// node seeds from and the one Holdings advertises. The directory is
// authoritative by design (madshare docs/architecture/madnetwork-cache.md), so
// a file placed here IS a held blob.
func (d *labDevice) seed(hash string, data []byte) {
	d.t.Helper()
	dir := filepath.Join(d.dir, "cache", "madnetwork")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hash), data, 0o644); err != nil {
		d.t.Fatal(err)
	}
}

// fetcher is the player's real download path: the same blobcache, the same
// swarm-then-nothing choice a madnetwork track gets in the app. Built after
// enrolAt, because the enrolment IS the vouch it presents.
func (d *labDevice) fetcher(home *labHome) *remote.Fetcher {
	d.t.Helper()
	cache, err := blobcache.Open(filepath.Join(d.dir, "remote"), 0)
	if err != nil {
		d.t.Fatal(err)
	}
	f := remote.New(cache, labLogger(d.name+"/fetch"))
	f.SetServers([]library.Server{{Base: home.URL(), Label: "home", Client: madclient.New(home.URL(), "lab-token")}})
	f.SetSwarm(d.be, d.enrol)
	f.SetSwarmBudget(8 * time.Second)
	return f
}

// waitLinkUp waits until the device's underlay peering to the hub is up —
// link-level connectivity, so a later refusal cannot be blamed on the dial.
func waitLinkUp(t *testing.T, d *labDevice) {
	t.Helper()
	waitUntil(t, 30*time.Second, d.name+" underlay up", func() (bool, string) {
		for _, p := range d.be.UnderlayPeers() {
			if p.Up {
				return true, ""
			}
		}
		return false, "no live peering"
	})
}

func waitUntil(t *testing.T, patience time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(patience)
	note := ""
	for time.Now().Before(deadline) {
		ok, n := cond()
		if ok {
			return
		}
		if n != "" {
			note = n
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s: not within %s (last: %s)", what, patience, note)
}

// labBlob is a deterministic pseudo-random blob and its content hash.
func labBlob(size int, seedNum int64) (hash string, data []byte) {
	data = make([]byte, size)
	mrand.New(mrand.NewSource(seedNum)).Read(data)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data
}

// networkItem is a queue row as browsing the madnetwork builds one: a hash, a
// size, a codec, the server that can name holders — and NO URL, because there
// is deliberately no relay behind it (fetch.go's fill).
func networkItem(home *labHome, hash string, size int64) *queue.Item {
	return &queue.Item{
		Network: true,
		Hash:    hash,
		Base:    home.URL(),
		Origin:  "lab",
		Size:    size,
		Codec:   "mp3",
		Title:   "lab track",
	}
}

// fetchUntil retries a fetch until it lands, with patience for the two clocks
// a fresh lab pays once: yggdrasil route convergence and the holder's
// membership memo (Intervals.MembershipTTL, one minute — a home node added
// after the memo was built is invisible until it expires).
func fetchUntil(t *testing.T, f *remote.Fetcher, item *queue.Item, patience time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(patience)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		path, err := f.Local(ctx, item)
		cancel()
		if err == nil {
			return path
		}
		last = err
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("fetch of %.12s… never succeeded within %s: %v", item.Hash, patience, last)
	return ""
}

// fetchNever asserts the fetch keeps failing for the whole window, and returns
// the last error — what the player would have shown the person.
func fetchNever(t *testing.T, f *remote.Fetcher, item *queue.Item, window time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(window)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		path, err := f.Local(ctx, item)
		cancel()
		if err == nil {
			t.Fatalf("fetch of %.12s… unexpectedly succeeded (%s)", item.Hash, path)
		}
		last = err
		time.Sleep(2 * time.Second)
	}
	return last
}

func labSkip(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a whole madnetwork in-process; skipped with -short")
	}
}

// ── The scenarios ────────────────────────────────────────────────────────────

// TestLabFreshMadnetworkServesANetworkTrack is the baseline the other
// scenarios lean on: a brand-new network, one seeder vouched for by the same
// home as the player, one madnetwork track — and the bytes arrive over the
// swarm, byte-identical. If THIS fails, nothing else in the lab means
// anything.
func TestLabFreshMadnetworkServesANetworkTrack(t *testing.T) {
	labSkip(t)
	hub := startLabHub(t)
	home := newLabHome(t, hub)
	seeder := startLabDevice(t, "seeder", hub)
	player := startLabDevice(t, "player", hub)

	hash, blob := labBlob(3<<20, 1)
	seeder.seed(hash, blob)
	home.offer(hash, seeder.node.Key(), int64(len(blob)))

	seeder.enrolAt(home)
	player.enrolAt(home)

	f := player.fetcher(home)
	path := fetchUntil(t, f, networkItem(home, hash, int64(len(blob))), 3*time.Minute)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fetched track: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("fetched %d byte(s), want %d, and/or contents differ", len(got), len(blob))
	}
}

// TestLabAnExpiredVouchDeclinesHonestlyAndRenewsItselfBack pins the fix for
// the reported failure this scenario used to reproduce.
//
// The state under test is the one a device wakes into after a suspend, a
// frozen app, or an hour with the home server unreachable: the token it holds
// has expired on the wall clock while the enrolment's own monotonic clocks
// say renewal is not due. Before the fix, Present handed the corpse out,
// every holder 404ed it — deliberately without saying why — and a madnetwork
// track failed looking like nobody held it (the skip the owner reported; the
// original reproduction is in this file's history, and the component half in
// internal/mesh/enrolment_expiry_test.go).
//
// Since the fix, enrolment refuses a wall-clock-dead vouch: the fetch
// declines with the honest "no vouch from X yet" instead of burning the
// budget on refusals, and the refusal marks the server due and wakes the
// loop — so the moment the home server issues living grants again, playback
// heals ITSELF, with nobody re-signing-in.
func TestLabAnExpiredVouchDeclinesHonestlyAndRenewsItselfBack(t *testing.T) {
	labSkip(t)
	hub := startLabHub(t)
	home := newLabHome(t, hub)
	seeder := startLabDevice(t, "seeder", hub)
	player := startLabDevice(t, "player", hub)

	hash1, blob1 := labBlob(2<<20, 2)
	hash2, blob2 := labBlob(2<<20, 3)
	seeder.seed(hash1, blob1)
	seeder.seed(hash2, blob2)
	home.offer(hash1, seeder.node.Key(), int64(len(blob1)))
	home.offer(hash2, seeder.node.Key(), int64(len(blob2)))

	seeder.enrolAt(home)
	player.enrolAt(home)

	// Prove the pipe first, so nothing below can be blamed on convergence.
	f := player.fetcher(home)
	fetchUntil(t, f, networkItem(home, hash1, int64(len(blob1))), 3*time.Minute)

	// Now the player's vouch goes wall-clock dead.
	home.setStale(true)
	e := player.enrolAt(home)
	if e.Present(home.URL()) {
		t.Fatal("Present offered a wall-clock-dead vouch — the expiry check regressed")
	}

	// Fetches fail — but honestly, and without touching the swarm.
	f2 := player.fetcher(home)
	err := fetchNever(t, f2, networkItem(home, hash2, int64(len(blob2))), 20*time.Second)
	t.Logf("with a dead vouch the player is told: %v", err)
	if err == nil || !strings.Contains(err.Error(), "no vouch from") {
		t.Fatalf("the decline should say the vouch is missing, got: %v", err)
	}

	// The server heals. NOTHING on the player is restarted or re-enrolled by
	// hand: the next fetch's refusal nudges the loop, the loop renews, and the
	// fetch after that succeeds — the recovery the fix was built to provide.
	home.setStale(false)
	fetchUntil(t, f2, networkItem(home, hash2, int64(len(blob2))), 2*time.Minute)
}

// TestLabAHolderThatCannotPlaceTheVouchingServerServesNothing is the
// "not directly connected by the auth system" case: the track's holder is
// enrolled under a DIFFERENT home server, so the vouch the player presents
// names an issuer the holder cannot place — and the holder serves nothing,
// with both machines healthy and the route fine. On the real network the same
// arm refuses whenever the holder's view of the community does not (yet, or
// any more) contain the player's home server: a listener-node holder from
// another household, or a member whose gossip has not reached that far.
//
// Phase two gives the holder a reason to trust the issuer — it signs in to
// that server too — and the very same fetch succeeds, pinning placement as
// the only variable.
func TestLabAHolderThatCannotPlaceTheVouchingServerServesNothing(t *testing.T) {
	labSkip(t)
	hub := startLabHub(t)
	homeA := newLabHome(t, hub) // the player's home
	homeB := newLabHome(t, hub) // the seeder's home
	seeder := startLabDevice(t, "seeder", hub)
	player := startLabDevice(t, "player", hub)

	hash, blob := labBlob(2<<20, 4)
	seeder.seed(hash, blob)
	homeA.offer(hash, seeder.node.Key(), int64(len(blob)))

	seeder.enrolAt(homeB)
	player.enrolAt(homeA)
	waitLinkUp(t, seeder)
	waitLinkUp(t, player)

	f := player.fetcher(homeA)
	item := networkItem(homeA, hash, int64(len(blob)))
	err := fetchNever(t, f, item, 45*time.Second)
	t.Logf("with an unplaceable issuer the player is told: %v", err)

	// The moment the seeder can place homeA, the same fetch works.
	seeder.enrolAt(homeA, homeB)
	fetchUntil(t, f, item, 3*time.Minute)
}

// TestLabATruncatedSeederCopyIsRefusedBeforeItPlays pins the fix for what
// used to be "it plays the first seconds, then skips" (option A of the skip
// diagnosis, owner's call 2026-08-23; madshare federation-swarm.md §"Two
// manifest hardenings", the size cross-check).
//
// The seeder's cached copy is a correct PREFIX of the blob under its full
// hash. A sole holder's manifest used to be believed outright, so its
// self-consistent description of the truncated file let every chunk verify
// and stream into playback — the track audibly started — until the
// whole-file hash ended it mid-listen. A blob's size is pinned by its
// content hash, and the player advertises the catalog's size with the fetch,
// so a sole manifest that contradicts it is now refused BEFORE a byte moves:
// the track fails immediately, with a sentence naming the contradiction,
// instead of starting something that cannot finish. (A mid-stream death of a
// HEALTHY holder is the other road to the old symptom, and that one is
// answered by the resumes — internal/remote/resume_test.go.)
func TestLabATruncatedSeederCopyIsRefusedBeforeItPlays(t *testing.T) {
	labSkip(t)
	hub := startLabHub(t)
	home := newLabHome(t, hub)
	seeder := startLabDevice(t, "seeder", hub)
	player := startLabDevice(t, "player", hub)

	// Warm the pipe with a healthy track, so the phase under test starts from
	// a converged network and a warm membership memo.
	hash1, blob1 := labBlob(1<<20, 5)
	seeder.seed(hash1, blob1)
	home.offer(hash1, seeder.node.Key(), int64(len(blob1)))
	seeder.enrolAt(home)
	player.enrolAt(home)
	f := player.fetcher(home)
	fetchUntil(t, f, networkItem(home, hash1, int64(len(blob1))), 3*time.Minute)

	// The truncated copy: right bytes, wrong length, full hash.
	hash2, blob2 := labBlob(3<<20, 6)
	seeder.seed(hash2, blob2[:900_000])
	home.offer(hash2, seeder.node.Key(), int64(len(blob2)))
	item := networkItem(home, hash2, int64(len(blob2)))

	// Generous only for the swarm's own budget arithmetic — the refusal itself
	// is immediate, and the resumes never fire (nothing was written).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var n int64
	rc, _, err := f.Stream(ctx, item)
	if err == nil {
		// The cache layer may hand the reader over before the fill has failed;
		// the refusal then arrives on the first read. Either way, no byte.
		n, err = io.Copy(io.Discard, rc)
		rc.Close()
	}
	if err == nil {
		t.Fatalf("the truncated copy streamed to completion (%d bytes) — nothing refused it and nothing verified it", n)
	}
	if n != 0 {
		t.Fatalf("%d byte(s) played before the failure (%v) — the refusal is supposed to land BEFORE the first byte", n, err)
	}
	if !strings.Contains(err.Error(), "advertised") {
		t.Fatalf("the failure should name the size contradiction, got: %v", err)
	}
	t.Logf("the truncated copy was refused up front with: %v", err)

	// And no poisoned copy is kept: a replay tomorrow must not skip.
	if f.Cached(item) {
		t.Fatalf("the failed fetch left a cached copy behind — every replay would fail from disk")
	}
}
