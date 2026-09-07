package backend

// The node key: backing it up and restoring it.
//
// The PEM file madshare keeps at <data dir>/federation.key IS this node's
// identity — the mesh address derives from it, every friendship in the peer
// table names it, every claim this node publishes is signed by it. Lose the
// file and the device comes back as a stranger: every pairing must be redone
// and every published claim is orphaned. A server's operator knows the file
// exists; a person running a player does not, and the plan that graduated
// pairing into node mode (madshare docs/plans/full-node-mode.md, P6) asks for
// the backup to be offered where the key's consequence is visible, with the
// warning in plain words, and for a restore path on a fresh install.
//
// The facade has no key surface and none is asked for: the file is the
// contract (config/mesh.go: "the one file that must never move"), yggdrasil's
// own PEM — PKCS#8, ed25519 — is what it holds, and the standard library reads
// that. Copying the file out and putting one back are the two acts; both stay
// file operations here, so a person who prefers a terminal gets the same
// result with cp.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrSameKey is RestoreKey's answer when the file offered holds the key this
// node already runs on: nothing to do, and worth saying so rather than
// "restored", which would send the person off to restart for nothing.
var ErrSameKey = errors.New("that is already this node's key")

// KeyFile is the path of the node's identity key, or "" when this backend runs
// no mesh (a player with the switch off has no node and no key to back up).
func (b *Backend) KeyFile() string { return b.keyFile }

// BackUpKey copies the node key to dst — a typed path, ~ allowed — and returns
// the path written. The copy is 0600, as the original is. The same key already
// there is a no-op that still answers with the path (a backup pressed twice
// is one backup, the materialize rule); a DIFFERENT file there is refused
// rather than replaced, because the only thing that lives at a path somebody
// keeps node keys at is another node key.
func (b *Backend) BackUpKey(dst string) (string, error) {
	if b.keyFile == "" {
		return "", errors.New("this device runs no node — switch the madnetwork on first")
	}
	cur, err := os.ReadFile(b.keyFile)
	if err != nil {
		return "", fmt.Errorf("read the node key: %w", err)
	}
	if _, err := parseNodeKey(cur); err != nil {
		return "", fmt.Errorf("the node key at %s is not readable as one: %w", b.keyFile, err)
	}
	abs, err := filepath.Abs(expandHome(dst))
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(abs); err == nil {
		if st.IsDir() {
			abs = filepath.Join(abs, filepath.Base(b.keyFile))
		} else {
			have, err := os.ReadFile(abs)
			if err != nil {
				return "", err
			}
			if bytes.Equal(have, cur) {
				return abs, nil
			}
			return "", fmt.Errorf("%s already exists and is not this node's key — pick another name", abs)
		}
	}
	if have, err := os.ReadFile(abs); err == nil {
		// The directory branch above may have landed on an existing copy.
		if bytes.Equal(have, cur) {
			return abs, nil
		}
		return "", fmt.Errorf("%s already exists and is not this node's key — pick another name", abs)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(abs, cur, 0600); err != nil {
		return "", err
	}
	return abs, nil
}

// RestoreKey makes the key in the PEM file at src this node's identity, from
// the next start on: the running node keeps the key it was started with, so
// the caller owes the person a restart. The key being replaced is kept beside
// the key file as federation.key.replaced-<time> — an identity is not
// something to lose by pressing the wrong button — and PendingKey reports the
// restored key's public half until the restart happens. Returns that public
// key, lowercase hex, the form the pairing page shows.
func (b *Backend) RestoreKey(src string) (string, error) {
	if b.keyFile == "" {
		return "", errors.New("this device runs no node — switch the madnetwork on first")
	}
	abs, err := filepath.Abs(expandHome(src))
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	priv, err := parseNodeKey(raw)
	if err != nil {
		return "", fmt.Errorf("%s is not a node key: %w", abs, err)
	}
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if cur, err := os.ReadFile(b.keyFile); err == nil {
		if curPriv, err := parseNodeKey(cur); err == nil && bytes.Equal(curPriv, priv) {
			return pub, ErrSameKey
		}
		// Keep what is being replaced. A restore that fails between here and
		// the rename leaves both files in place, which is the safe failure.
		kept := b.keyFile + ".replaced-" + time.Now().UTC().Format("20060102-150405")
		if err := writeFileAtomic(kept, cur, 0600); err != nil {
			return "", fmt.Errorf("keep the current key aside: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read the current node key: %w", err)
	}
	if err := writeFileAtomic(b.keyFile, raw, 0600); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.pendingKey = pub
	b.mu.Unlock()
	return pub, nil
}

// PendingKey is the public key of a restored identity that waits for the next
// start, or "" when the node runs the key its file holds.
func (b *Backend) PendingKey() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pendingKey
}

// parseNodeKey reads yggdrasil's key PEM — a PKCS#8 "PRIVATE KEY" block
// holding an ed25519 key, which is what config.NodeConfig.MarshalPEMPrivateKey
// writes and UnmarshalPEMPrivateKey demands — and returns the key.
func parseNodeKey(raw []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("PEM block is %q, not a private key", block.Type)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an ed25519 key")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("unexpected ed25519 key length")
	}
	return priv, nil
}

// writeFileAtomic writes via a sibling temp file and a rename, so a crash
// mid-write never leaves a half key where the whole one was.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
