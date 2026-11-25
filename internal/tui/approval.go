package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/zhimoe/aglet/internal/permissions"
)

// ProgramHolder lets the approver reach the running Bubble Tea program after
// both the runtime and the model have been constructed.
type ProgramHolder struct {
	Program *tea.Program
}

type approvalRequestMsg struct {
	req  permissions.ApprovalRequest
	resp chan permissions.Decision
}

// Approver bridges a tool approval request into the TUI.
type Approver struct {
	holder *ProgramHolder
}

func NewApprover(holder *ProgramHolder) *Approver {
	return &Approver{holder: holder}
}

func (a *Approver) Approve(ctx context.Context, req permissions.ApprovalRequest) (permissions.Decision, error) {
	if a.holder == nil || a.holder.Program == nil {
		return permissions.DecisionDeny, fmt.Errorf("tui is not ready to approve %s", req.ToolName)
	}
	resp := make(chan permissions.Decision, 1)
	a.holder.Program.Send(approvalRequestMsg{req: req, resp: resp})
	select {
	case d := <-resp:
		return d, nil
	case <-ctx.Done():
		return permissions.DecisionDeny, ctx.Err()
	}
}
