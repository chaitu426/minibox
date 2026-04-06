package models

// InstructionType defines the type of build instruction
type InstructionType string

const (
	TypeFrom    InstructionType = "FROM"
	TypeWorkdir InstructionType = "WORKDIR"
	TypeCopy    InstructionType = "COPY"
	TypeRun     InstructionType = "RUN"
	TypeCmd     InstructionType = "CMD"
	TypeEnv     InstructionType = "ENV"
	TypePort    InstructionType = "PORT"
	TypeUser    InstructionType = "USER"
	TypeVolume  InstructionType = "VOLUME"
)

// Instruction represents a single operation inside a Block
type Instruction struct {
	Type InstructionType
	Args []string
}

// Block represents a named, independently buildable unit in the Boxfile DAG
type Block struct {
	Name         string
	Needs        []string // block names this block depends on
	Instructions []Instruction
	AutoDeps     bool // whether to run auto-dependency detection
}

// Cfile holds all extracted metadata required to build an image (Phase 5: DAG syntax)
type Cfile struct {
	BaseImage string
	Blocks    []*Block
	Cmd       []string
	Env       map[string]string
	Workdir   string
	// Basic healthcheck support parsed from MiniBox.
	HealthcheckCmd         []string
	HealthcheckIntervalSec int
	// Legacy flat instructions (not used in new syntax but keep for compat)
	Instructions []Instruction
}
