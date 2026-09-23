// Package lmstudio reads the chats of the LM Studio desktop app (local models).
//
// Store: one whole-file JSON document per chat,
// %USERPROFILE%\.lmstudio\conversations\<epoch-ms>.conversation.json, possibly
// inside sub-folders (the app's chat folders). The LM Studio home can be moved;
// the app then writes its path to %USERPROFILE%\.lmstudio-home-pointer, which is
// honoured. The old home %USERPROFILE%\.cache\lm-studio is also looked at.
//
// Format (verified against LM Studio 0.4.x files, structure only):
//   - NativeID = file name without ".conversation.json" (the creation time in ms).
//   - name: the chat's name (null, "" or the placeholder "Untitled" when unnamed).
//     LM Studio names chats itself and the user may rename them; the file does
//     not say which, so a name counts as an agent title. Unnamed chats get the
//     first user prompt.
//   - createdAt, userLastMessagedAt, assistantLastMessagedAt: epoch ms.
//   - messages[]: {versions[], currentlySelected}. Regenerated answers and edited
//     prompts are extra versions; only the selected one is read.
//   - a version is either {type:"singleStep", role, content[]} (user; parts
//     {type:"text", text} or {type:"file", fileIdentifier, fileType}) or
//     {type:"multiStep", role:"assistant", steps[]}. Steps of type "contentBlock"
//     carry content[] text fragments that are concatenated; blocks with
//     style.type "thinking" are reasoning and are dropped, as are fragments with
//     isStructural (chat-template tokens). "debugInfoBlock" steps are dropped.
//     stepIdentifier is "<epoch-ms>-<random>" and gives the assistant message time;
//     user messages carry no time.
//   - Tool calls use the lmstudio-js shapes ({type:"toolCallRequest",
//     toolCallRequest:{name, arguments}} and {type:"toolCallResult", content});
//     no local chat had tool calls, so this part is unverified.
//   - clientInput (the unsent draft), systemPrompt and model settings are not read.
//
// Chats have no working folder (Cwd is empty, project "(no project)"). There is no
// way to open a given chat from the command line, so "resume" just starts the app.
// Chats with no messages (new, never used) are not sessions.
package lmstudio

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AbuDergham/Athar/internal/adapters"
	"github.com/AbuDergham/Athar/internal/fsread"
)

const (
	// convSuffix is the file name suffix of a chat file.
	convSuffix = ".conversation.json"
	// settle is how long a whole-file JSON source must be unchanged before it is
	// read (spec §4.3); younger files are left for the next sync.
	settle = 5 * time.Second
	// maxPointerSize bounds the home-pointer file (it holds one path).
	maxPointerSize = 4096
)

type adapter struct {
	// now is the clock used for the settle rule; nil means time.Now.
	now func() time.Time
}

// New returns the LM Studio adapter (used by internal/registry).
func New() adapters.Adapter { return adapter{now: time.Now} }

func (adapter) ID() string   { return "lmstudio" }
func (adapter) Name() string { return "LM Studio" }
func (adapter) Version() int { return 1 }

// Caps: no terminal resume; Enter starts the LM Studio app, where the user
// picks the chat from the sidebar.
func (adapter) Caps() adapters.Caps {
	return adapters.Caps{
		App: &adapters.AppCommand{
			Name:    "LM Studio",
			Program: "LM Studio.exe",
			Candidates: []string{
				`%LOCALAPPDATA%\Programs\LM Studio\LM Studio.exe`,
				`%ProgramFiles%\LM Studio\LM Studio.exe`,
			},
		},
	}
}

// Sources lists every *.conversation.json under <home>\conversations (recursively)
// for each LM Studio home. Missing folders are skipped silently; a conversations
// folder that exists but cannot be listed is reported as an error, together with
// whatever could be listed elsewhere.
func (adapter) Sources(env adapters.Env) ([]adapters.Source, error) {
	var (
		out  []adapters.Source
		errs []error
	)
	for _, home := range homes(env) {
		dir := filepath.Join(home, "conversations")
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == dir && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				return nil // missing folder, or an unreadable sub-folder: skip it
			}
			if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), convSuffix) {
				return nil
			}
			out = append(out, adapters.Source{Adapter: "lmstudio", Path: p, Files: []string{p}})
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("lmstudio: list %s: %w", dir, err))
		}
	}
	return out, errors.Join(errs...)
}

// homes returns the LM Studio home folders to look in, without duplicates:
// ~\.lmstudio, the one named by ~\.lmstudio-home-pointer and ~\.cache\lm-studio.
// The default home comes first so that its spelling is kept (source paths stay
// stable) when the pointer names the same folder.
func homes(env adapters.Env) []string {
	if env.Home == "" {
		return nil
	}
	var out []string
	add := func(p string) {
		p = filepath.Clean(p)
		for _, q := range out {
			if sameDir(p, q) {
				return
			}
		}
		out = append(out, p)
	}
	add(filepath.Join(env.Home, ".lmstudio"))
	if p := pointedHome(env.Home); p != "" {
		add(p)
	}
	add(filepath.Join(env.Home, ".cache", "lm-studio"))
	return out
}

// pointedHome reads ~\.lmstudio-home-pointer (a file holding the path of a
// relocated LM Studio home). It returns "" unless it names an existing folder
// by absolute path.
func pointedHome(home string) string {
	p := filepath.Join(home, ".lmstudio-home-pointer")
	size, _, err := fsread.Stat(p)
	if err != nil || size == 0 || size > maxPointerSize {
		return ""
	}
	b, err := fsread.ReadFile(p)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(strings.TrimPrefix(string(b), string(rune(0xFEFF))))
	if s == "" || strings.ContainsAny(s, "\x00\r\n") || !filepath.IsAbs(s) {
		return ""
	}
	if fi, err := os.Stat(s); err != nil || !fi.IsDir() {
		return ""
	}
	return s
}

// sameDir reports whether a and b are the same folder (same path, or both
// exist and are the same file system object, which covers case differences).
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// Read parses one chat file. A file changed less than 5 s ago, or one that is not
// valid JSON (for example caught mid-write), fails the source so that the next
// sync reads it again; malformed messages inside a valid file are skipped and counted.
func (a adapter) Read(ctx context.Context, env adapters.Env, src adapters.Source) (adapters.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return adapters.ReadResult{}, err
	}
	name := filepath.Base(src.Path)
	if len(name) <= len(convSuffix) || !strings.HasSuffix(strings.ToLower(name), convSuffix) {
		return adapters.ReadResult{}, fmt.Errorf("lmstudio: %s is not a chat file", name)
	}
	id := name[:len(name)-len(convSuffix)]
	_, mtime, err := fsread.Stat(src.Path)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	if age := a.clock().Sub(mtime); age > -settle && age < settle {
		return adapters.ReadResult{}, fmt.Errorf("lmstudio: %s changed less than 5 s ago; it will be read on the next sync", name)
	}
	data, err := fsread.ReadFile(src.Path)
	if err != nil {
		return adapters.ReadResult{}, err
	}
	s, skipped, err := parse(data, id, mtime)
	if err != nil {
		return adapters.ReadResult{}, fmt.Errorf("lmstudio: %s: %w", name, err)
	}
	res := adapters.ReadResult{Skipped: skipped}
	if s != nil {
		res.Sessions = append(res.Sessions, *s)
	}
	return res, nil
}

func (a adapter) clock() time.Time {
	if a.now == nil {
		return time.Now()
	}
	return a.now()
}
