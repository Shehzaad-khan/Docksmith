package build

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Instruction is the sealed interface — every parsed Docksmithfile line is one of these.
type Instruction interface {
	Kind() string // "FROM", "COPY", "RUN", "WORKDIR", "ENV", "CMD"
	Raw() string  // verbatim line as written in the Docksmithfile
	Line() int    // 1-based line number
}

// base holds the fields shared by every instruction type.
type base struct {
	raw     string
	lineNum int
}

// Raw returns the original instruction text exactly as written in the file.
func (b base) Raw() string { return b.raw }

// Line returns the 1-based line number used for user-facing parse errors.
func (b base) Line() int   { return b.lineNum }

// FromInstr: FROM <image>[:<tag>]
type FromInstr struct {
	base
	Image string // e.g. "alpine"
	Tag   string // e.g. "3.18"; defaults to "latest"
}

// Kind identifies this parsed instruction as FROM.
func (f FromInstr) Kind() string { return "FROM" }

// CopyInstr: COPY <src>... <dest>
// Srcs contains everything except the last token; Dest is the last token.
// Supports * globs (resolved at build time); ** is a planned extension.
type CopyInstr struct {
	base
	Srcs []string
	Dest string
}

// Kind identifies this parsed instruction as COPY.
func (c CopyInstr) Kind() string { return "COPY" }

// RunInstr: RUN <command>
// Command is the entire rest-of-line shell string.
type RunInstr struct {
	base
	Command string
}

// Kind identifies this parsed instruction as RUN.
func (r RunInstr) Kind() string { return "RUN" }

// WorkdirInstr: WORKDIR <path>
type WorkdirInstr struct {
	base
	Path string
}

// Kind identifies this parsed instruction as WORKDIR.
func (w WorkdirInstr) Kind() string { return "WORKDIR" }

// EnvInstr: ENV <KEY>=<VALUE>
type EnvInstr struct {
	base
	Key   string
	Value string
}

// Kind identifies this parsed instruction as ENV.
func (e EnvInstr) Kind() string { return "ENV" }

// CmdInstr: CMD ["exec","arg",...]
// Parts is decoded from the required JSON array form.
type CmdInstr struct {
	base
	Parts []string
}

// Kind identifies this parsed instruction as CMD.
func (c CmdInstr) Kind() string { return "CMD" }

// ParseFile reads a Docksmithfile at path and returns a slice of Instructions.
// Any unrecognised instruction causes an immediate error with the line number.
// Blank lines and lines starting with '#' are silently skipped.
func ParseFile(path string) ([]Instruction, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open Docksmithfile at %q: %w", path, err)
	}
	defer f.Close()

	var instructions []Instruction
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split into keyword + rest-of-line
		parts := strings.SplitN(line, " ", 2)
		keyword := strings.ToUpper(parts[0])
		rest := ""
		if len(parts) == 2 {
			rest = strings.TrimSpace(parts[1])
		}

		b := base{raw: line, lineNum: lineNum}

		switch keyword {
		case "FROM":
			instr, err := parseFrom(b, rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
			instructions = append(instructions, instr)

		case "COPY":
			instr, err := parseCopy(b, rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
			instructions = append(instructions, instr)

		case "RUN":
			if rest == "" {
				return nil, fmt.Errorf("line %d: RUN requires a command", lineNum)
			}
			instructions = append(instructions, RunInstr{base: b, Command: rest})

		case "WORKDIR":
			if rest == "" {
				return nil, fmt.Errorf("line %d: WORKDIR requires a path", lineNum)
			}
			instructions = append(instructions, WorkdirInstr{base: b, Path: rest})

		case "ENV":
			instr, err := parseEnv(b, rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
			instructions = append(instructions, instr)

		case "CMD":
			instr, err := parseCmd(b, rest)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNum, err)
			}
			instructions = append(instructions, instr)

		default:
			// Hard requirement: unknown instruction → immediate error with line number
			return nil, fmt.Errorf("line %d: unknown instruction %q", lineNum, keyword)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading Docksmithfile: %w", err)
	}

	return instructions, nil
}

// ── per-instruction parsers ──────────────────────────────────────────────────

// parseFrom validates FROM syntax and applies the default tag when omitted.
// It rejects empty image names and malformed image:tag combinations.
func parseFrom(b base, rest string) (FromInstr, error) {
	if rest == "" {
		return FromInstr{}, fmt.Errorf("FROM requires an image name")
	}
	image, tag := rest, "latest"
	if idx := strings.Index(rest, ":"); idx != -1 {
		image = rest[:idx]
		tag = rest[idx+1:]
	}
	if image == "" {
		return FromInstr{}, fmt.Errorf("FROM: image name cannot be empty")
	}
	if tag == "" {
		return FromInstr{}, fmt.Errorf("FROM: tag cannot be empty (omit ':' to use 'latest')")
	}
	return FromInstr{base: b, Image: image, Tag: tag}, nil
}

// parseCopy splits COPY arguments into source list and destination path.
// At least one source and one destination token are required.
func parseCopy(b base, rest string) (CopyInstr, error) {
	if rest == "" {
		return CopyInstr{}, fmt.Errorf("COPY requires at least one source and a destination")
	}
	tokens := strings.Fields(rest)
	if len(tokens) < 2 {
		return CopyInstr{}, fmt.Errorf("COPY requires at least one source and a destination, got only %q", rest)
	}
	return CopyInstr{
		base: b,
		Srcs: tokens[:len(tokens)-1],
		Dest: tokens[len(tokens)-1],
	}, nil
}

// parseEnv validates and splits ENV in KEY=VALUE form.
// The key must be non-empty to avoid ambiguous runtime behavior.
func parseEnv(b base, rest string) (EnvInstr, error) {
	idx := strings.Index(rest, "=")
	if idx <= 0 {
		return EnvInstr{}, fmt.Errorf("ENV requires KEY=VALUE format, got %q", rest)
	}
	return EnvInstr{
		base:  b,
		Key:   rest[:idx],
		Value: rest[idx+1:],
	}, nil
}

// parseCmd validates CMD JSON-array syntax and decodes command parts.
// This enforces exec-form commands for predictable runtime execution.
func parseCmd(b base, rest string) (CmdInstr, error) {
	if rest == "" {
		return CmdInstr{}, fmt.Errorf("CMD requires a JSON array argument")
	}
	var parts []string
	if err := json.Unmarshal([]byte(rest), &parts); err != nil {
		return CmdInstr{}, fmt.Errorf(`CMD must use JSON array form like ["exec","arg"]: %w`, err)
	}
	if len(parts) == 0 {
		return CmdInstr{}, fmt.Errorf("CMD array cannot be empty")
	}
	return CmdInstr{base: b, Parts: parts}, nil
}
