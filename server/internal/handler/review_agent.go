package handler

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// reviewAgentSettingKey is the workspace settings JSON key holding the agent
// that takes over issues an agent moves into review. Empty or absent means the
// workspace has no automatic review handoff.
const reviewAgentSettingKey = "review_agent_id"

// RegisterReviewAgentListeners subscribes the review handoff to issue updates.
// It runs on the same bus as the activity and autopilot listeners so the
// reassignment it performs is observed by them like any other assignee change.
func (h *Handler) RegisterReviewAgentListeners() {
	h.Bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		h.handleReviewHandoff(context.Background(), e)
	})
}

// handleReviewHandoff reassigns an issue to the workspace's review agent when
// an AGENT moves it into the in_review category. A member moving an issue to
// review is choosing where it goes and is left alone; an agent moving it there
// is reporting that its work is done and the workspace's review policy
// applies.
//
// The reassignment is published as a second issue:updated event with a system
// actor and status_changed=false, so this listener never re-enters itself and
// the activity log records the handoff as an ordinary assignee change.
func (h *Handler) handleReviewHandoff(ctx context.Context, e events.Event) {
	if e.ActorType != "agent" || e.ActorID == "" {
		return
	}
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return
	}
	if statusChanged, _ := payload["status_changed"].(bool); !statusChanged {
		return
	}
	resp, ok := payload["issue"].(IssueResponse)
	if !ok {
		return
	}
	issueID, err := util.ParseUUID(resp.ID)
	if err != nil {
		return
	}

	// Re-read the row rather than trusting the payload: the handoff writes
	// assignee and must not resurrect fields from a stale response.
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return
	}
	if issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status) != "in_review" {
		return
	}

	reviewAgentID, ok := h.reviewAgentForWorkspace(ctx, issue.WorkspaceID)
	if !ok {
		return
	}
	// The review agent finishing its own review and leaving the issue in
	// review must not hand the issue back to itself.
	if e.ActorID == uuidToString(reviewAgentID) {
		return
	}
	if issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" &&
		uuidToString(issue.AssigneeID) == uuidToString(reviewAgentID) {
		return
	}
	agent, err := h.Queries.GetAgent(ctx, reviewAgentID)
	if err != nil || agent.ArchivedAt.Valid ||
		uuidToString(agent.WorkspaceID) != uuidToString(issue.WorkspaceID) {
		return
	}

	// Pre-fill the bare-narg columns like UpdateIssue does, and pin the
	// revision so a concurrent edit wins over this background write.
	updated, err := h.Queries.UpdateIssue(ctx, db.UpdateIssueParams{
		ID:               issue.ID,
		ExpectedRevision: pgtype.Int8{Int64: issue.Revision, Valid: true},
		AssigneeType:     pgtype.Text{String: "agent", Valid: true},
		AssigneeID:       reviewAgentID,
		StartDate:        issue.StartDate,
		DueDate:          issue.DueDate,
		ParentIssueID:    issue.ParentIssueID,
		ProjectID:        issue.ProjectID,
		Stage:            issue.Stage,
	})
	if err != nil {
		slog.Warn("review handoff: reassign failed",
			"issue_id", uuidToString(issue.ID),
			"review_agent_id", uuidToString(reviewAgentID),
			"error", err)
		return
	}

	prefix := ""
	if ws, wsErr := h.Queries.GetWorkspace(ctx, issue.WorkspaceID); wsErr == nil {
		prefix = ws.IssuePrefix
	}
	out := issueToResponse(updated, prefix)
	h.fillStatusCategory(ctx, updated.WorkspaceID, &out)
	h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "system", "", map[string]any{
		"issue":              out,
		"assignee_changed":   true,
		"status_changed":     false,
		"prev_assignee_type": textToPtr(issue.AssigneeType),
		"prev_assignee_id":   uuidToPtr(issue.AssigneeID),
		"prev_status":        issue.Status,
		"creator_type":       issue.CreatorType,
		"creator_id":         uuidToString(issue.CreatorID),
	})

	// Same decision and dispatch as a member reassigning the issue by hand.
	// No request is in scope, so the probe carries no self-loop or
	// self-assignment guard; both concern the acting agent, and the review
	// agent is by construction not the actor here.
	trigger, ok := h.IssueService.WillEnqueueRun(ctx, service.IssueTriggerInput{
		Issue:           updated,
		PrevStatus:      issue.Status,
		AssigneeChanged: true,
	}, service.IssueTriggerProbe{})
	if ok {
		h.dispatchIssueRun(ctx, updated, trigger, "system", "", "")
	}
	slog.Info("review agent auto-assigned",
		"issue_id", uuidToString(issue.ID),
		"review_agent_id", uuidToString(reviewAgentID),
		"workspace_id", uuidToString(issue.WorkspaceID),
		"run_enqueued", ok)
}

// reviewAgentForWorkspace reads the configured review agent from the
// workspace settings JSON. It reports false when the setting is absent, empty,
// or not a UUID.
func (h *Handler) reviewAgentForWorkspace(ctx context.Context, workspaceID pgtype.UUID) (pgtype.UUID, bool) {
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil || len(ws.Settings) == 0 {
		return pgtype.UUID{}, false
	}
	var settings map[string]any
	if err := json.Unmarshal(ws.Settings, &settings); err != nil {
		return pgtype.UUID{}, false
	}
	raw, _ := settings[reviewAgentSettingKey].(string)
	if raw == "" {
		return pgtype.UUID{}, false
	}
	id, err := util.ParseUUID(raw)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return id, true
}
