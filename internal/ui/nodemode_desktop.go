//go:build !android

package ui

// nodeModeOffered: desktops may become regular member nodes. The owner's rule
// (full-node-mode.md, 2026-08-22) is desktop only — a phone as a member drains
// its battery holding the mesh up and churns everybody's availability
// machinery — so the android twin of this file says false.
const nodeModeOffered = true
