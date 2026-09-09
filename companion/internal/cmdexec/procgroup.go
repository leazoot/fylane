package cmdexec

import osexec "os/exec"

// ProcessGroup exposes this package's process-group handling to the other
// places in the Core that start a child of their own.
//
// It exists because the reason for a group is not specific to running one
// command: a child that spawns children — a language server invoking the Go
// toolchain, say — leaves them behind when only the direct child is signalled,
// and those orphans keep holding the pipes. Every long-lived child in the Core
// should be wrapped the same way rather than each inventing its own.
//
// The zero value is not usable; call NewProcessGroup.
type ProcessGroup struct{ g *procGroup }

// NewProcessGroup returns a group for one child process.
func NewProcessGroup() *ProcessGroup { return &ProcessGroup{g: newProcGroup()} }

// Prepare configures cmd before it is started. It must be called before Start.
func (p *ProcessGroup) Prepare(cmd *osexec.Cmd) { p.g.prepare(cmd) }

// Adopt takes ownership of the started process. It must be called right after
// Start; on Windows a process spawned in the gap between the two escapes.
func (p *ProcessGroup) Adopt(cmd *osexec.Cmd) error { return p.g.adopt(cmd) }

// Kill takes down the whole tree, asking before insisting.
func (p *ProcessGroup) Kill(cmd *osexec.Cmd) error { return p.g.kill(cmd) }

// Close releases the group's own resources. It does not kill anything on Unix;
// on Windows closing the job handle takes the tree with it, which is the point.
func (p *ProcessGroup) Close() { p.g.close() }
