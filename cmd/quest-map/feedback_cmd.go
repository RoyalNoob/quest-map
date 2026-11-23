package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/RoyalNoob/quest-map/internal/mcpproxy"
	"github.com/RoyalNoob/quest-map/internal/mcpserver"
	"github.com/RoyalNoob/quest-map/internal/server"
)

// cmdFeedback files a feedback report through the board server, starting it if needed. Build machines and
// other computers use the intake listener instead (spec §8.2).
func cmdFeedback(args []string, stdout, stderr io.Writer) int {
	root, rest, err := boardRoot(args)
	if err != nil {
		fmt.Fprintln(stderr, "quest-map feedback:", err)
		return 1
	}
	var in mcpserver.FeedbackIn
	var text []string
	shot := ""
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		val := func() string {
			if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "--") {
				return v
			}
			if i+1 < len(rest) {
				i++
				return rest[i]
			}
			return ""
		}
		switch {
		case a == "--project" || strings.HasPrefix(a, "--project="):
			in.Project = val()
		case a == "--release" || strings.HasPrefix(a, "--release=") || a == "--build" || strings.HasPrefix(a, "--build="):
			in.Release = val()
		case a == "--reporter" || strings.HasPrefix(a, "--reporter="):
			in.Reporter = val()
		case a == "--screenshot" || strings.HasPrefix(a, "--screenshot="):
			shot = val()
		case strings.HasPrefix(a, "--"):
			fmt.Fprintf(stderr, "quest-map feedback: unknown option %s\n", a)
			return 2
		default:
			text = append(text, a)
		}
	}
	in.Text = strings.Join(text, " ")
	in.Via = "the CLI"
	if strings.TrimSpace(in.Text) == "" {
		fmt.Fprintln(stderr, `usage: quest-map feedback "what happened" [--project P] [--release ID] [--screenshot FILE] [--reporter NAME]`)
		return 2
	}
	if shot != "" {
		data, err := os.ReadFile(shot)
		if err != nil {
			fmt.Fprintln(stderr, "quest-map feedback:", err)
			return 1
		}
		in.ScreenshotB64 = base64.StdEncoding.EncodeToString(data)
		in.ScreenshotName = filepath.Base(shot)
	}
	exe, _ := os.Executable()
	info, err := mcpproxy.EnsureServer(root, exe, 20e9)
	if err != nil {
		fmt.Fprintln(stderr, "quest-map feedback:", err)
		return 1
	}
	var out map[string]string
	if err := server.CallJSON(info, "POST", "/api/feedback", in, &out); err != nil {
		fmt.Fprintln(stderr, "quest-map feedback:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Filed %s as %s in %s.\n", out["kind"], out["id"], out["project"])
	if w := out["warning"]; w != "" {
		fmt.Fprintln(stderr, "warning:", w)
	}
	return 0
}
