package ui

import (
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/layout"

	"daemonlord.ygg/madplayer/internal/about"
)

// The About section at the foot of Settings: what this build is, whose it is,
// under what licence, and how to get its source.
//
// It is the licence's section rather than a credits screen. madplayer links
// madshare, which is AGPL-3.0-or-later, so this program is too — and the AGPL
// wants the notice shown and the Corresponding Source offered. Section 13 is the
// one that makes it more than a formality here: a madnetwork node SERVES blobs
// to other people's devices, so there are users interacting with this program
// remotely over a network, which is exactly who that section is about.
//
// The source is OFFERED, not shipped. Carrying a copy of the tree inside the
// binary would answer the licence and cost tens of megabytes on the one device
// where that matters most — a phone. §6d allows the offer to be access from a
// network server instead, which is what the address below is for; until it
// answers, §6b's written offer (ask the author) is what stands, and the panel
// says which of the two is in force rather than pointing at a 404 and calling it
// compliance (internal/about).
func (a *App) aboutControls(gtx C) D {
	b := a.build

	if a.btnCopySource.Clicked(gtx) {
		gtx.Execute(clipboard.WriteCmd{
			Type: "application/text",
			Data: io.NopCloser(strings.NewReader(about.SourceURL)),
		})
		a.setNotice("Address copied — " + about.SourceURL)
	}
	if a.btnCopyBuild.Clicked(gtx) {
		gtx.Execute(clipboard.WriteCmd{
			Type: "application/text",
			Data: io.NopCloser(strings.NewReader(b.Notice())),
		})
		a.setNotice("Build details copied")
	}

	return layout.Inset{Top: 18, Bottom: 4}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return a.sectionTitle(gtx, "About madplayer") }),

			// What this build IS. The commit is not decoration: source that does
			// not correspond to the binary is not Corresponding Source, so the
			// offer below is only worth something because this line is here.
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx, strings.Join([]string{
					b.BuildLine(),
					"Embeds madshare " + b.Madshare,
					b.Go + " · " + b.Platform,
				}, "  ·  "))
			}),

			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx, "A music player for your own machine and your own network. "+
					"Copyright © 2026 "+about.Author+".")
			}),

			// The notice the licence asks for, in its own words.
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx, "This is free software: you may use, study, share and change it "+
					"under the "+about.License+". "+about.Warranty)
			}),

			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(func(gtx C) D { return a.sectionTitle(gtx, "Source code") }),
			layout.Rigid(func(gtx C) D { return a.sectionHint(gtx, b.SourceOffer()) }),
			layout.Rigid(func(gtx C) D {
				return a.sectionHint(gtx, about.SourceURL)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return a.smallButton(gtx, &a.btnCopySource, "Copy address", false)
						}),
						layout.Rigid(layout.Spacer{Width: 8}.Layout),
						// Copying the whole notice is what somebody actually needs
						// when reporting a bug or asking for the matching source:
						// the address alone does not say which commit.
						layout.Rigid(func(gtx C) D {
							return a.smallButton(gtx, &a.btnCopyBuild, "Copy build details", false)
						}),
					)
				})
			}),
		)
	})
}
