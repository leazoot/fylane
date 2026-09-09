package app

import (
	"context"

	"github.com/leazoot/fylane/companion/internal/lsp"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// impactFromLSP answers the engine's impact question using the language
// servers this Companion already has running.
//
// It is a thin adapter on purpose. The rule that makes it safe lives in
// lsp.Reach — only a warm server is asked, because starting one needs the
// user's answer and this code runs inside a prompt already asking them
// something else.
type impactFromLSP struct{ sup *lsp.Supervisor }

func (i impactFromLSP) Impact(ctx context.Context, req txn.ImpactRequest) *txn.OpImpact {
	if i.sup == nil {
		return nil
	}
	reach, err := i.sup.Reach(ctx, lsp.Query{
		WorkspaceID: req.WorkspaceID, Root: req.Root, Path: req.Path,
	}, req.Lines)
	if err != nil || reach == nil {
		// See txn.Impacter for why this is not an error path: the write must
		// not fail over a number, absence is already the honest report, and
		// the error text carries a workspace path, which does not go to a log.
		return nil
	}
	return &txn.OpImpact{Symbols: reach.Symbols, Callers: reach.Callers, Partial: reach.Partial}
}
