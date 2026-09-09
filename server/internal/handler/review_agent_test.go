package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// setReviewAgent stores agentID as the workspace's review agent for the
// duration of the test and removes the key afterwards.
func setReviewAgent(t *testing.T, agentID string) {
	t.Helper()
	dbfx.Exec(t,
		`UPDATE workspace SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{review_agent_id}', to_jsonb($1::text)) WHERE id = $2`,
		agentID, testWorkspaceID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(),
			`UPDATE workspace SET settings = settings - 'review_agent_id' WHERE id = $1`, testWorkspaceID)
	})
}

// reviewHandoffEvent builds the issue:updated event UpdateIssue publishes when
// actor moves issueID into its current status.
func reviewHandoffEvent(t *testing.T, issueID, actorType, actorID, prevStatus string) events.Event {
	t.Helper()
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issueID))
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	return events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: testWorkspaceID,
		ActorType:   actorType,
		ActorID:     actorID,
		Payload: map[string]any{
			"issue":          issueToResponse(issue, "TES"),
			"status_changed": true,
			"prev_status":    prevStatus,
		},
	}
}

func reviewAssigneeOf(t *testing.T, issueID string) string {
	t.Helper()
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issueID))
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	return uuidToString(issue.AssigneeID)
}

func pendingRuns(t *testing.T, issueID, agentID string) int {
	t.Helper()
	return dbfx.Count(t,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status IN ('queued', 'dispatched')`,
		issueID, agentID)
}

func TestReviewHandoff(t *testing.T) {
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, "review handoff runtime")
	worker := dbfx.Agent(t, "review handoff worker", runtimeID)
	reviewer := dbfx.Agent(t, "review handoff reviewer", runtimeID)

	newIssue := func(t *testing.T, status string) string {
		id := dbfx.Issue(t, "review handoff "+status, testutil.Cols{
			"status":        status,
			"assignee_type": "agent",
			"assignee_id":   worker,
		})
		dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, id)
		dbfx.Cleanup(t, `DELETE FROM activity_log WHERE issue_id = $1`, id)
		return id
	}

	t.Run("agent moving an issue to in_review hands it to the review agent", func(t *testing.T) {
		setReviewAgent(t, reviewer)
		issueID := newIssue(t, "in_review")

		testHandler.handleReviewHandoff(ctx, reviewHandoffEvent(t, issueID, "agent", worker, "in_progress"))

		if got := reviewAssigneeOf(t, issueID); got != reviewer {
			t.Fatalf("assignee = %s, want review agent %s", got, reviewer)
		}
		if n := pendingRuns(t, issueID, reviewer); n != 1 {
			t.Fatalf("pending runs for review agent = %d, want 1", n)
		}
	})

	t.Run("custom status in the in_review category also hands off", func(t *testing.T) {
		setReviewAgent(t, reviewer)
		dbfx.Insert(t, "issue_status", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"key":          "review_handoff_custom",
			"name":         "Human review",
			"category":     "in_review",
			"color":        "#888888",
			"position":     42,
		})
		issueID := newIssue(t, "review_handoff_custom")

		testHandler.handleReviewHandoff(ctx, reviewHandoffEvent(t, issueID, "agent", worker, "in_progress"))

		if got := reviewAssigneeOf(t, issueID); got != reviewer {
			t.Fatalf("assignee = %s, want review agent %s", got, reviewer)
		}
	})

	t.Run("member moving an issue to in_review is left alone", func(t *testing.T) {
		setReviewAgent(t, reviewer)
		issueID := newIssue(t, "in_review")

		testHandler.handleReviewHandoff(ctx, reviewHandoffEvent(t, issueID, "member", testUserID, "in_progress"))

		if got := reviewAssigneeOf(t, issueID); got != worker {
			t.Fatalf("assignee = %s, want unchanged worker %s", got, worker)
		}
		if n := pendingRuns(t, issueID, reviewer); n != 0 {
			t.Fatalf("pending runs for review agent = %d, want 0", n)
		}
	})

	t.Run("the review agent itself does not re-trigger", func(t *testing.T) {
		setReviewAgent(t, reviewer)
		issueID := newIssue(t, "in_review")

		testHandler.handleReviewHandoff(ctx, reviewHandoffEvent(t, issueID, "agent", reviewer, "in_progress"))

		if got := reviewAssigneeOf(t, issueID); got != worker {
			t.Fatalf("assignee = %s, want unchanged worker %s", got, worker)
		}
	})

	t.Run("statuses outside the in_review category are ignored", func(t *testing.T) {
		setReviewAgent(t, reviewer)
		issueID := newIssue(t, "done")

		testHandler.handleReviewHandoff(ctx, reviewHandoffEvent(t, issueID, "agent", worker, "in_progress"))

		if got := reviewAssigneeOf(t, issueID); got != worker {
			t.Fatalf("assignee = %s, want unchanged worker %s", got, worker)
		}
	})

	t.Run("no review agent configured means no handoff", func(t *testing.T) {
		issueID := newIssue(t, "in_review")

		testHandler.handleReviewHandoff(ctx, reviewHandoffEvent(t, issueID, "agent", worker, "in_progress"))

		if got := reviewAssigneeOf(t, issueID); got != worker {
			t.Fatalf("assignee = %s, want unchanged worker %s", got, worker)
		}
	})

	t.Run("registered listener reacts to the bus without re-entering itself", func(t *testing.T) {
		setReviewAgent(t, reviewer)
		issueID := newIssue(t, "in_review")

		h := *testHandler
		h.Bus = events.New()
		h.RegisterReviewAgentListeners()
		published := 0
		h.Bus.Subscribe(protocol.EventIssueUpdated, func(events.Event) { published++ })

		h.Bus.Publish(reviewHandoffEvent(t, issueID, "agent", worker, "in_progress"))

		if got := reviewAssigneeOf(t, issueID); got != reviewer {
			t.Fatalf("assignee = %s, want review agent %s", got, reviewer)
		}
		// The original event plus exactly one handoff event.
		if published != 2 {
			t.Fatalf("issue:updated events seen = %d, want 2", published)
		}
	})
}
