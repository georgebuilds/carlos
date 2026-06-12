package web

import (
	"context"
	"errors"
	"testing"
)

func TestGroupStore_CreateListMemberCounts(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	seedThread(t, log, "t1", "thread one", "hello")
	seedThread(t, log, "t2", "thread two", "hi")
	seedThread(t, log, "t3", "thread three", "yo")

	web, err := gs.Create(ctx, "carlos web")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if web.Pos != 0 {
		t.Errorf("first group pos = %d, want 0", web.Pos)
	}
	anneal, _ := gs.Create(ctx, "anneal")
	if anneal.Pos != 1 {
		t.Errorf("second group pos = %d, want 1", anneal.Pos)
	}

	// Two threads into web, one into anneal.
	mustSet(t, gs, "t1", &web.ID)
	mustSet(t, gs, "t2", &web.ID)
	mustSet(t, gs, "t3", &anneal.ID)

	groups, err := gs.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	// Ordered by pos: web first.
	if groups[0].Name != "carlos web" || groups[0].Threads != 2 {
		t.Errorf("group[0] = %+v, want carlos web with 2 members", groups[0])
	}
	if groups[1].Name != "anneal" || groups[1].Threads != 1 {
		t.Errorf("group[1] = %+v, want anneal with 1 member", groups[1])
	}
}

func TestGroupStore_MembershipMapHidesVanishedThreads(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	seedThread(t, log, "live", "alive", "hi")
	g, _ := gs.Create(ctx, "g")
	mustSet(t, gs, "live", &g.ID)
	// A membership row for a thread that was never inserted as an agent:
	// the join must hide it (plan §4.6).
	mustSet(t, gs, "ghost", &g.ID)

	m, err := gs.MembershipMap(ctx)
	if err != nil {
		t.Fatalf("membership map: %v", err)
	}
	if _, ok := m["live"]; !ok {
		t.Error("live thread missing from membership map")
	}
	if _, ok := m["ghost"]; ok {
		t.Error("ghost thread should be hidden by the agents join")
	}
	// Member count likewise ignores the ghost.
	got, _ := gs.List(ctx)
	if got[0].Threads != 1 {
		t.Errorf("member count = %d, want 1 (ghost excluded)", got[0].Threads)
	}

	// Sweep removes the orphan row.
	n, err := gs.SweepOrphans(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d rows, want 1", n)
	}
}

func TestGroupStore_DeleteRevertsMembers(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	seedThread(t, log, "t1", "one", "hi")
	g, _ := gs.Create(ctx, "doomed")
	mustSet(t, gs, "t1", &g.ID)

	if err := gs.Delete(ctx, g.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Thread still exists; it is just ungrouped now.
	m, _ := gs.MembershipMap(ctx)
	if _, ok := m["t1"]; ok {
		t.Error("member should have reverted to ungrouped after delete")
	}
	groups, _ := gs.List(ctx)
	if len(groups) != 0 {
		t.Errorf("got %d groups after delete, want 0", len(groups))
	}
}

func TestGroupStore_PatchRenameAndReposition(t *testing.T) {
	_, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	g, _ := gs.Create(ctx, "old name")
	newName := "new name"
	newPos := 5
	patched, err := gs.Patch(ctx, g.ID, &newName, &newPos)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if patched.Name != "new name" || patched.Pos != 5 {
		t.Errorf("patched = %+v, want new name / pos 5", patched)
	}

	// Empty name is rejected.
	empty := ""
	if _, err := gs.Patch(ctx, g.ID, &empty, nil); err == nil {
		t.Error("patch with empty name should error")
	}
}

func TestGroupStore_NotFoundPaths(t *testing.T) {
	_, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	if _, err := gs.Patch(ctx, "nope", strptr("x"), nil); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("patch unknown: got %v, want ErrGroupNotFound", err)
	}
	if err := gs.Delete(ctx, "nope"); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("delete unknown: got %v, want ErrGroupNotFound", err)
	}
	// Moving a thread into a nonexistent group is rejected.
	if err := gs.SetThreadGroup(ctx, "t1", strptr("nope")); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("set unknown group: got %v, want ErrGroupNotFound", err)
	}
}

func TestGroupStore_SetThreadGroupUpsertAndRemove(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	seedThread(t, log, "t1", "one", "hi")
	a, _ := gs.Create(ctx, "a")
	b, _ := gs.Create(ctx, "b")

	mustSet(t, gs, "t1", &a.ID)
	mustSet(t, gs, "t1", &b.ID) // re-assign (upsert)
	m, _ := gs.MembershipMap(ctx)
	if m["t1"] != b.ID {
		t.Errorf("after reassign, t1 in %s, want %s", m["t1"], b.ID)
	}

	// Remove from any group (nil).
	if err := gs.SetThreadGroup(ctx, "t1", nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	m, _ = gs.MembershipMap(ctx)
	if _, ok := m["t1"]; ok {
		t.Error("t1 should be ungrouped after nil set")
	}
}

func mustSet(t *testing.T, gs *GroupStore, threadID string, groupID *string) {
	t.Helper()
	if err := gs.SetThreadGroup(context.Background(), threadID, groupID); err != nil {
		t.Fatalf("set thread group %s: %v", threadID, err)
	}
}
