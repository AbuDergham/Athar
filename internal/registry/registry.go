// Package registry lists every adapter. Adapters live in their own packages
// under internal/adapters/<id>/ so they can be developed independently.
// Adding an agent = one package + one line here + testdata fixtures.
package registry

import (
	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/adapters/agy"
	"github.com/AbuDergham/Athar/internal/adapters/antigravity"
	"github.com/AbuDergham/Athar/internal/adapters/claude"
	"github.com/AbuDergham/Athar/internal/adapters/codebuff"
	"github.com/AbuDergham/Athar/internal/adapters/codex"
	"github.com/AbuDergham/Athar/internal/adapters/commandcode"
	"github.com/AbuDergham/Athar/internal/adapters/continuedev"
	"github.com/AbuDergham/Athar/internal/adapters/copilot"
	"github.com/AbuDergham/Athar/internal/adapters/cursor"
	"github.com/AbuDergham/Athar/internal/adapters/dsh"
	"github.com/AbuDergham/Athar/internal/adapters/gemini"
	"github.com/AbuDergham/Athar/internal/adapters/hermes"
	"github.com/AbuDergham/Athar/internal/adapters/imports"
	"github.com/AbuDergham/Athar/internal/adapters/kilo"
	"github.com/AbuDergham/Athar/internal/adapters/lmstudio"
	"github.com/AbuDergham/Athar/internal/adapters/neovate"
	"github.com/AbuDergham/Athar/internal/adapters/opencode"
	"github.com/AbuDergham/Athar/internal/adapters/qoder"
	"github.com/AbuDergham/Athar/internal/adapters/qwen"
	"github.com/AbuDergham/Athar/internal/adapters/trae"
	"github.com/AbuDergham/Athar/internal/adapters/vscodetasks"
	"github.com/AbuDergham/Athar/internal/adapters/windsurf"
	"github.com/AbuDergham/Athar/internal/adapters/zcode"
	"github.com/AbuDergham/Athar/internal/adapters/zed"
)

// All returns every adapter, most specific path roots first.
//
// Two adapters share the ID "kilo": Kilo 7 (kilo.db) and the legacy VS Code
// extension tasks. Caps are looked up by agent id with the last adapter
// winning, so the legacy adapter comes last: its "open in the editor" action
// works for both kinds of session, while `kilo --session` only fits kilo.db ids.
func All() []adapters.Adapter {
	return []adapters.Adapter{
		claude.New(),
		codex.New(),
		gemini.New(),
		qwen.New(),
		qoder.New(),
		neovate.New(),
		commandcode.New(),
		dsh.New(),
		codebuff.New(),
		agy.New(),
		antigravity.New(),
		opencode.New(),
		kilo.New(),
		zcode.New(),
		hermes.New(),
		zed.New(),
		vscodetasks.NewCline(),
		copilot.New(),
		cursor.New(),
		trae.New(),
		continuedev.New(),
		lmstudio.New(),
		windsurf.New(),
		vscodetasks.NewKiloLegacy(),
		imports.NewAgentsView(),
		imports.NewPSM(),
	}
}
