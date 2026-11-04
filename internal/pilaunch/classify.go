package pilaunch

import (
	"fmt"
	"strings"
)

type Route string

const (
	Passthrough Route = "passthrough"
	Wrapped     Route = "wrapped"
	Resume      Route = "resume"
	Refused     Route = "refused"
)

type Classification struct {
	Kind        Route
	Reason      string
	SessionFile string
	SessionDir  string
}

var valuedOptions = map[string]bool{
	"--provider": true, "--model": true, "--models": true, "--thinking": true,
	"--system-prompt": true, "--append-system-prompt": true, "--name": true, "-n": true,
	"--session-dir": true, "--tools": true, "-t": true, "--exclude-tools": true, "-xt": true,
	"--extension": true, "-e": true, "--skill": true, "--prompt-template": true,
	"--theme": true,
}

var switches = map[string]bool{
	"--no-tools": true, "-nt": true, "--no-builtin-tools": true, "-nbt": true, "--no-mcp": true,
	"--no-skills": true, "-ns": true, "--no-prompt-templates": true, "-np": true,
	"--no-themes": true, "--no-context-files": true, "-nc": true, "--verbose": true,
	"--approve": true, "-a": true, "--no-approve": true, "-na": true, "--offline": true,
}

var refusedSelectors = map[string]bool{
	"--session-id": true, "--continue": true, "-c": true, "--fork": true,
}

var passthroughOptions = map[string]bool{
	"install": true, "remove": true, "uninstall": true, "update": true, "list": true,
	"config": true, "auth": true, "mcp": true,
}

// Classify applies the pinned Pi 1.4.0 option-consumption rules. eligible is true
// only for the caller's validated Kitty TTY context.
func Classify(argv []string, eligible bool) Classification {
	if !eligible {
		return Classification{Kind: Passthrough}
	}
	if len(argv) > 0 && passthroughOptions[argv[0]] {
		return Classification{Kind: Passthrough}
	}
	finalMode := "text"
	resume := false
	otherArguments := false
	sessionDirSet := false
	sessionFile, sessionDir := "", ""
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			otherArguments = true
			break
		}
		if arg == "--help" || arg == "-h" || arg == "--version" || arg == "-v" || arg == "--export" || arg == "--list-models" {
			return Classification{Kind: Passthrough}
		}
		if arg == "--mode" {
			otherArguments = true
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "-") {
				return Classification{Kind: Refused, Reason: "malformed --mode"}
			}
			i++
			if argv[i] != "text" && argv[i] != "json" && argv[i] != "rpc" {
				return Classification{Kind: Refused, Reason: "unsupported --mode value"}
			}
			finalMode = argv[i]
			continue
		}
		if arg == "--print" {
			return Classification{Kind: Passthrough}
		}
		if arg == "-p" {
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "@") && (!strings.HasPrefix(argv[i+1], "-") || strings.HasPrefix(argv[i+1], "---")) {
				i++
			}
			return Classification{Kind: Passthrough}
		}
		if arg == "--resume" || arg == "-r" || arg == "--session" {
			if resume {
				return Classification{Kind: Refused, Reason: "choose exactly one resume selector"}
			}
			resume = true
			if arg == "--session" {
				if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "-") {
					return Classification{Kind: Refused, Reason: "--session requires an existing file path"}
				}
				i++
				sessionFile = argv[i]
				if !strings.Contains(sessionFile, "/") && !strings.HasSuffix(sessionFile, ".jsonl") {
					return Classification{Kind: Refused, Reason: "--session requires a file path, not an ID"}
				}
			}
			continue
		}
		if refusedSelectors[arg] {
			return Classification{Kind: Refused, Reason: "session selectors are not supported for managed launch"}
		}
		if arg == "--api-key" || arg == "--no-session" || arg == "--no-extensions" || arg == "-ne" {
			return Classification{Kind: Passthrough}
		}
		if arg == "--use-theme" || arg == "--tui-mode" {
			otherArguments = true
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "-") {
				return Classification{Kind: Refused, Reason: fmt.Sprintf("malformed %s", arg)}
			}
			i++
			continue
		}
		if valuedOptions[arg] {
			if i+1 >= len(argv) {
				return Classification{Kind: Refused, Reason: fmt.Sprintf("malformed %s", arg)}
			}
			i++
			if arg == "--session-dir" {
				sessionDirSet = true
				sessionDir = argv[i]
			} else {
				otherArguments = true
			}
			continue
		}
		if switches[arg] {
			otherArguments = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return Classification{Kind: Refused, Reason: "unsupported or unknown option"}
		}
		otherArguments = true
	}
	if finalMode == "json" || finalMode == "rpc" {
		return Classification{Kind: Passthrough}
	}
	if resume {
		if otherArguments || sessionDirSet && sessionDir == "" {
			return Classification{Kind: Refused, Reason: "resume accepts only one selector and optional nonempty --session-dir"}
		}
		return Classification{Kind: Resume, SessionFile: sessionFile, SessionDir: sessionDir}
	}
	return Classification{Kind: Wrapped}
}
