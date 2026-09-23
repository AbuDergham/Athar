package tui

// menuInput is what decides which action-menu entries are shown (spec §2.3).
type menuInput struct {
	cliResume   bool   // a terminal resume command exists (agent caps or athar.toml)
	app         string // name of the GUI app that "resume" opens ("" = none)
	canFork     bool   // the agent supports forking
	canSkip     bool   // the agent has a skip-permissions flag
	local       bool   // the session has a live source on this machine (§4.6)
	live        bool   // running now (●, §6.5)
	interactive bool   // the running session is interactive (not a background one)
	folder      bool   // the project folder exists
	command     bool   // a copyable command exists
}

// menuEntry is one line of the action menu.
type menuEntry struct {
	id    menuItemID
	key   string // shown key (the Arabic-layout key is accepted too)
	label string
}

const warnTwoWriters = "Open in another window: resuming here means two processes write one transcript."

func (in menuInput) showResume() bool {
	return (in.cliResume || in.app != "") && in.local && !(in.live && !in.interactive)
}

func (in menuInput) showFork() bool { return in.live && in.local && in.canFork && in.cliResume }

// skipAction is the action "!" runs: fork when fork is listed first (a live
// session), resume otherwise.
func skipAction(in menuInput) menuItemID {
	if in.showFork() {
		return itemFork
	}
	return itemResume
}

// menuEntries lists the applicable entries in display order (fork first when
// the session is live) and the warning line to show, if any.
func menuEntries(in menuInput) ([]menuEntry, string) {
	var es []menuEntry
	showR, showF := in.showResume(), in.showFork()
	if showF {
		es = append(es, menuEntry{itemFork, "F", "Fork: resume as a new session"})
	}
	if showR {
		label := "Resume in a new tab"
		if !in.cliResume {
			label = "Open the project in " + in.app
		}
		es = append(es, menuEntry{itemResume, "R", label})
	}
	if in.canSkip && ((showR && in.cliResume) || showF) {
		label := "Resume without permission prompts"
		if skipAction(in) == itemFork {
			label = "Fork without permission prompts"
		}
		es = append(es, menuEntry{itemSkip, "!", label})
	}
	es = append(es, menuEntry{itemRead, "O", "Read the conversation in the browser"})
	if in.command {
		es = append(es, menuEntry{itemCopy, "C", "Copy the command (PowerShell)"})
	}
	if in.local && in.folder {
		es = append(es, menuEntry{itemFolder, "D", "Open the project folder"})
	}
	warning := ""
	if showR && in.live && in.interactive {
		warning = warnTwoWriters
	}
	return es, warning
}
