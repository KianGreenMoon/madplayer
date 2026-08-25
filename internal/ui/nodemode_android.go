//go:build android

package ui

// Phones stay listener nodes (full-node-mode.md, owner 2026-08-22): a member
// must hold the mesh up to be worth its slot in everybody's discovery budget,
// and a phone is asleep most of the day. Not offering the switch removes the
// whole mode from the build — the section, its page, and the pages it gates.
const nodeModeOffered = false
