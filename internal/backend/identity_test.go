package backend

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nodeKeyPEM writes a fresh ed25519 key the way yggdrasil does — PKCS#8 in a
// "PRIVATE KEY" block — and returns the PEM with the public half in hex.
func nodeKeyPEM(t *testing.T) ([]byte, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), hex.EncodeToString(pub)
}

// keyedBackend is a backend with a key file and nothing else: the identity
// acts are file operations, and starting a whole node to test them would
// test the node.
func keyedBackend(t *testing.T) (*Backend, string) {
	t.Helper()
	dir := t.TempDir()
	raw, pub := nodeKeyPEM(t)
	path := filepath.Join(dir, "federation.key")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return &Backend{keyFile: path}, pub
}

func TestBackUpKeyCopiesTheFileAndIsIdempotent(t *testing.T) {
	b, _ := keyedBackend(t)
	dst := filepath.Join(t.TempDir(), "keys", "player.key")

	got, err := b.BackUpKey(dst)
	if err != nil {
		t.Fatalf("BackUpKey: %v", err)
	}
	if got != dst {
		t.Fatalf("wrote to %s, asked for %s", got, dst)
	}
	want, _ := os.ReadFile(b.keyFile)
	have, _ := os.ReadFile(dst)
	if string(have) != string(want) {
		t.Fatalf("the backup is not the key")
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0600 {
		t.Fatalf("backup mode %v, want 0600 — a private key is not for the group", st.Mode().Perm())
	}
	// Pressed twice is one backup.
	if _, err := b.BackUpKey(dst); err != nil {
		t.Fatalf("second backup to the same path: %v", err)
	}
	// A directory means "in there, under the key's own name".
	d := t.TempDir()
	got, err = b.BackUpKey(d)
	if err != nil || got != filepath.Join(d, "federation.key") {
		t.Fatalf("backup into a directory: %s, %v", got, err)
	}
}

func TestBackUpKeyRefusesToReplaceAnotherFile(t *testing.T) {
	b, _ := keyedBackend(t)
	other, _ := nodeKeyPEM(t)
	dst := filepath.Join(t.TempDir(), "player.key")
	if err := os.WriteFile(dst, other, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := b.BackUpKey(dst)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a different file at the target should be refused, got: %v", err)
	}
	have, _ := os.ReadFile(dst)
	if string(have) != string(other) {
		t.Fatalf("the refused backup still overwrote the file")
	}
}

func TestBackUpKeyWithoutAMeshSaysSo(t *testing.T) {
	b := &Backend{}
	if _, err := b.BackUpKey(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("no key file should mean no node, got: %v", err)
	}
	if _, err := b.RestoreKey(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("no key file should mean no node, got: %v", err)
	}
}

func TestRestoreKeyReplacesTheIdentityAndKeepsTheOldOne(t *testing.T) {
	b, oldPub := keyedBackend(t)
	old, _ := os.ReadFile(b.keyFile)
	raw, newPub := nodeKeyPEM(t)
	src := filepath.Join(t.TempDir(), "backup.key")
	if err := os.WriteFile(src, raw, 0600); err != nil {
		t.Fatal(err)
	}

	pub, err := b.RestoreKey(src)
	if err != nil {
		t.Fatalf("RestoreKey: %v", err)
	}
	if pub != newPub {
		t.Fatalf("restored key reported as %s, want %s", pub, newPub)
	}
	have, _ := os.ReadFile(b.keyFile)
	if string(have) != string(raw) {
		t.Fatalf("the key file does not hold the restored key")
	}
	if st, _ := os.Stat(b.keyFile); st.Mode().Perm() != 0600 {
		t.Fatalf("key file mode %v after restore, want 0600", st.Mode().Perm())
	}
	if b.PendingKey() != newPub {
		t.Fatalf("PendingKey = %q, want the restored key until the next start", b.PendingKey())
	}
	// The replaced identity is kept beside the key file, not lost.
	kept, _ := filepath.Glob(b.keyFile + ".replaced-*")
	if len(kept) != 1 {
		t.Fatalf("expected one kept copy of the old key, found %v", kept)
	}
	keptRaw, _ := os.ReadFile(kept[0])
	if string(keptRaw) != string(old) {
		t.Fatalf("the kept copy is not the old key")
	}
	_ = oldPub
}

func TestRestoreKeyRefusesWhatIsNotAKey(t *testing.T) {
	b, _ := keyedBackend(t)
	before, _ := os.ReadFile(b.keyFile)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RestoreKey(src); err == nil || !strings.Contains(err.Error(), "not a node key") {
		t.Fatalf("a certificate is not a key, got: %v", err)
	}
	if _, err := b.RestoreKey(filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Fatalf("a missing file should fail")
	}
	after, _ := os.ReadFile(b.keyFile)
	if string(after) != string(before) {
		t.Fatalf("a refused restore touched the key file")
	}
	if b.PendingKey() != "" {
		t.Fatalf("a refused restore left a pending key")
	}
}

func TestRestoreKeyWithTheSameKeyIsNotARestore(t *testing.T) {
	b, pub := keyedBackend(t)
	dst := filepath.Join(t.TempDir(), "same.key")
	if _, err := b.BackUpKey(dst); err != nil {
		t.Fatal(err)
	}
	got, err := b.RestoreKey(dst)
	if !errors.Is(err, ErrSameKey) {
		t.Fatalf("restoring the running key should say so, got: %v", err)
	}
	if got != pub {
		t.Fatalf("reported %s, want %s", got, pub)
	}
	if kept, _ := filepath.Glob(b.keyFile + ".replaced-*"); len(kept) != 0 {
		t.Fatalf("a no-op restore kept a copy: %v", kept)
	}
	if b.PendingKey() != "" {
		t.Fatalf("a no-op restore left a pending key")
	}
}
