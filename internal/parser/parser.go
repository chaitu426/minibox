package parser

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/chaitu426/minibox/internal/models"
)

// ParseBoxfile reads a MiniBox Boxfile and parses it into a models.Cfile struct.
// It supports the new DAG-based syntax: BASE, BLOCK, NEED, START.
// For backwards-compatibility it also supports the old linear syntax (BOX, RUN, COPY, etc.)
func ParseBoxfile(filepath string) (*models.Cfile, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open Boxfile: %w", err)
	}
	defer file.Close()

	cfile := &models.Cfile{Env: make(map[string]string)}
	scanner := bufio.NewScanner(file)

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Detect format: new (BASE keyword) or legacy (BOX / FROM)
	for _, l := range lines {
		fields := strings.Fields(l)
		if len(fields) == 0 {
			continue
		}
		t := strings.ToUpper(fields[0])
		if t == "BASE" {
			return parseNewFormat(lines, cfile)
		}
		if t == "BOX" || t == "FROM" || t == "START-WITH" {
			return parseLegacyFormat(lines, cfile)
		}
	}
	return nil, fmt.Errorf("invalid Boxfile: missing BASE or BOX directive")
}

// ─── New DAG-based format ──────────────────────────────────────────────────

func parseNewFormat(lines []string, cfile *models.Cfile) (*models.Cfile, error) {
	var currentBlock *models.Block

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Top-level keyword (no leading whitespace)
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			parts := strings.Fields(trimmed)
			keyword := strings.ToUpper(parts[0])

			switch keyword {
			case "BASE":
				if len(parts) < 2 {
					return nil, fmt.Errorf("BASE requires an image argument")
				}
				cfile.BaseImage = parts[1]
				currentBlock = nil

			case "BLOCK":
				if len(parts) < 2 {
					return nil, fmt.Errorf("BLOCK requires a name")
				}
				currentBlock = &models.Block{Name: parts[1]}
				cfile.Blocks = append(cfile.Blocks, currentBlock)

			case "START":
				cfile.Cmd = parts[1:]
				currentBlock = nil

			case "HEALTHCHECK":
				// MiniBox simple form:
				// HEALTHCHECK <cmd...>
				// HEALTHCHECK --interval=30 <cmd...>
				cfile.HealthcheckIntervalSec = 30
				args := parts[1:]
				if len(args) > 0 && strings.HasPrefix(args[0], "--interval=") {
					if n, err := strconv.Atoi(strings.TrimPrefix(args[0], "--interval=")); err == nil && n > 0 {
						cfile.HealthcheckIntervalSec = n
					}
					args = args[1:]
				}
				if len(args) > 0 {
					cfile.HealthcheckCmd = args
				}
				currentBlock = nil

			default:
				// Unknown top-level — skip
			}
			continue
		}

		// Indented line — block instruction
		if currentBlock == nil {
			continue
		}

		parts := strings.Fields(trimmed)
		if len(parts) == 0 {
			continue
		}
		keyword := strings.ToUpper(parts[0])

		switch keyword {
		case "NEED":
			if len(parts) < 2 {
				return nil, fmt.Errorf("NEED requires a block name")
			}
			currentBlock.Needs = append(currentBlock.Needs, parts[1])

		case "BNEED":
			if len(parts) < 2 {
				return nil, fmt.Errorf("BNEED requires a block name")
			}
			currentBlock.BNeeds = append(currentBlock.BNeeds, parts[1])

		case "AUTO-DEPS":
			currentBlock.AutoDeps = true

		case "PKG":
			// pkg nodejs@20  →  apk add --no-cache nodejs=20*
			pkg, ver := parsePkgArg(parts[1:])
			args := []string{"apk", "add", "--no-cache"}
			if ver != "" {
				args = append(args, pkg+"="+ver+"*")
			} else {
				args = append(args, pkg)
			}
			currentBlock.Instructions = append(currentBlock.Instructions, models.Instruction{
				Type: models.TypeRun, Args: args,
			})

		case "RUN":
			currentBlock.Instructions = append(currentBlock.Instructions, models.Instruction{
				Type: models.TypeRun, Args: parts[1:],
			})

		case "COPY":
			if len(parts) >= 4 && strings.HasPrefix(strings.ToUpper(parts[1]), "FROM=") {
				fromBlock := strings.TrimPrefix(parts[1], "FROM=")
				currentBlock.BNeeds = append(currentBlock.BNeeds, fromBlock)
				currentBlock.Instructions = append(currentBlock.Instructions, models.Instruction{
					Type: models.TypeCopy, Args: parts[1:], // Keep FROM= in args so builder.go knows to copy from an external block
				})
			} else {
				currentBlock.Instructions = append(currentBlock.Instructions, models.Instruction{
					Type: models.TypeCopy, Args: parts[1:],
				})
			}

		case "WORKDIR":
			currentBlock.Instructions = append(currentBlock.Instructions, models.Instruction{
				Type: models.TypeWorkdir, Args: parts[1:],
			})
			cfile.Workdir = parts[1]

		case "ENV":
			// env KEY=VALUE or env KEY VALUE
			if len(parts) >= 3 && !strings.Contains(parts[1], "=") {
				cfile.Env[parts[1]] = parts[2]
			} else {
				for _, kv := range parts[1:] {
					p := strings.SplitN(kv, "=", 2)
					if len(p) == 2 {
						cfile.Env[p[0]] = p[1]
					}
				}
			}

		case "PORT":
			// port <num>  — stored as metadata only (no iptables rule here)
			// Future: could expose to networking layer

		case "USER":
			currentBlock.Instructions = append(currentBlock.Instructions, models.Instruction{
				Type: models.TypeUser, Args: parts[1:],
			})

		case "VOLUME":
			currentBlock.Instructions = append(currentBlock.Instructions, models.Instruction{
				Type: models.TypeVolume, Args: parts[1:],
			})
		}
	}

	if cfile.BaseImage == "" {
		return nil, fmt.Errorf("invalid Boxfile: missing BASE directive")
	}
	return cfile, nil
}

func parsePkgArg(args []string) (pkg, ver string) {
	if len(args) == 0 {
		return "", ""
	}
	p := strings.SplitN(args[0], "@", 2)
	pkg = p[0]
	if len(p) == 2 {
		ver = p[1]
	}
	return
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ─── Legacy linear format ──────────────────────────────────────────────────

func parseLegacyFormat(lines []string, cfile *models.Cfile) (*models.Cfile, error) {
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		instType := models.InstructionType(strings.ToUpper(parts[0]))
		args := []string{}
		if len(parts) > 1 {
			args = parts[1:]
		}

		// Aliases
		switch instType {
		case "BOX", "START-WITH":
			instType = models.TypeFrom
		case "PKG", "ADD-PACKAGE":
			instType = models.TypeRun
			args = append([]string{"apk", "add", "--no-cache"}, args...)
		case "INSTALL-DEPS":
			instType = models.TypeRun
			args = []string{"npm", "install"}
		case "CLONE-REPO":
			instType = models.TypeRun
			args = append([]string{"git", "clone"}, args...)
		case "RUN-COMMAND":
			instType = models.TypeRun
		case "GOTO-FOLDER", "WORKDIR":
			instType = models.TypeWorkdir
		case "SET-ENVIRONMENT", "ENV":
			instType = models.TypeEnv
		case "IMPORT-FILE":
			instType = models.TypeCopy
		case "GRAB-ALL", "SYNC-PACK":
			instType = models.TypeCopy
			args = []string{".", "/app"}
		case "LAUNCH":
			instType = models.TypeCmd
		}

		switch instType {
		case models.TypeFrom:
			cfile.BaseImage = args[0]
		case models.TypeEnv:
			if len(args) == 2 && !strings.Contains(args[0], "=") {
				cfile.Env[args[0]] = args[1]
			} else {
				for _, arg := range args {
					p := strings.SplitN(arg, "=", 2)
					if len(p) == 2 {
						cfile.Env[p[0]] = p[1]
					}
				}
			}
		case models.TypeWorkdir:
			cfile.Workdir = args[0]
			cfile.Instructions = append(cfile.Instructions, models.Instruction{Type: instType, Args: args})
		case models.TypeCmd:
			cfile.Cmd = args
		case "HEALTHCHECK":
			cfile.HealthcheckIntervalSec = 30
			if len(args) > 0 && strings.HasPrefix(args[0], "--interval=") {
				if n, err := strconv.Atoi(strings.TrimPrefix(args[0], "--interval=")); err == nil && n > 0 {
					cfile.HealthcheckIntervalSec = n
				}
				args = args[1:]
			}
			cfile.HealthcheckCmd = args
		default:
			cfile.Instructions = append(cfile.Instructions, models.Instruction{Type: instType, Args: args})
		}
	}

	if cfile.BaseImage == "" {
		return nil, fmt.Errorf("invalid Boxfile: missing FROM/BOX directive")
	}
	return cfile, nil
}
