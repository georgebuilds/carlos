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

	// live = the three seeded threads; member counts gate on this set.
	live := map[string]bool{"t1": true, "t2": true, "t3": true}
	groups, err := gs.List(ctx, live)
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

func TestGroupStore_MembershipMapAndLiveGatedCounts(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	seedThread(t, log, "live", "alive", "hi")
	g, _ := gs.Create(ctx, "g")
	mustSet(t, gs, "live", &g.ID)
	// A membership row for a thread that is not in the live roster (e.g.
	// janitor-pruned). Backend agnostic now: MembershipMap returns the row,
	// but the overlay only stamps ids already in the roster and the member
	// count is gated on the live set (plan WB-3).
	mustSet(t, gs, "ghost", &g.ID)

	m, err := gs.MembershipMap(ctx)
	if err != nil {
		t.Fatalf("membership map: %v", err)
	}
	if _, ok := m["live"]; !ok {
		t.Error("live thread missing from membership map")
	}
	// No agents join: the ghost row is returned. The overlay drops it because
	// it is not in the roster summaries.
	if _, ok := m["ghost"]; !ok {
		t.Error("ghost row should be returned (no agents join, backend agnostic)")
	}

	// Live-gated count counts only the roster member.
	live := map[string]bool{"live": true}
	got, _ := gs.List(ctx, live)
	if got[0].Threads != 1 {
		t.Errorf("live-gated member count = %d, want 1 (ghost excluded)", got[0].Threads)
	}
	// A nil live set applies no filter: both memberships count.
	gotAll, _ := gs.List(ctx, nil)
	if gotAll[0].Threads != 2 {
		t.Errorf("unfiltered member count = %d, want 2", gotAll[0].Threads)
	}

	// Sweep removes the orphan row (still keyed off the agents table; only
	// carlos threads have agent rows, so a carlos-only ghost is swept).
	n, err := gs.SweepOrphans(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d rows, want 1", n)
	}
}

func TestGroupStore_BackendAgnosticGrouping(t *testing.T) {
	_, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	// A Claude Code thread (no agent row) can be assigned to a group and
	// counted, proving the agents join is gone (plan WB-3).
	g, _ := gs.Create(ctx, "claude code")
	ccID := "cc:11111111-2222-3333-4444-555555555555"
	mustSet(t, gs, ccID, &g.ID)

	m, _ := gs.MembershipMap(ctx)
	if m[ccID] != g.ID {
		t.Errorf("cc thread membership = %q, want %q", m[ccID], g.ID)
	}
	live := map[string]bool{ccID: true}
	got, _ := gs.List(ctx, live)
	if got[0].Threads != 1 {
		t.Errorf("cc-only group count = %d, want 1", got[0].Threads)
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
	groups, _ := gs.List(ctx, nil)
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

// TestGroupStore_ClosedStoreErrors exercises the DB-error return paths of
// the new query methods (and a couple of touched ones) by closing the store.
func TestGroupStore_ClosedStoreErrors(t *testing.T) {
	_, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()
	// Create a group + memberships BEFORE closing so get/List have a target.
	g, _ := gs.Create(ctx, "g")
	mustSet(t, gs, "t1", &g.ID)
	gid := g.ID
	_ = gs.Close()

	if _, err := gs.List(ctx, nil); err == nil {
		t.Error("List on closed store should error")
	}
	if _, err := gs.MembershipMap(ctx); err == nil {
		t.Error("MembershipMap on closed store should error")
	}
	if _, err := gs.CCOriginSet(ctx); err == nil {
		t.Error("CCOriginSet on closed store should error")
	}
	if _, err := gs.memberCounts(ctx, nil); err == nil {
		t.Error("memberCounts on closed store should error")
	}
	// get is unexported; reach it through Patch with no fields (which calls get).
	if _, err := gs.Patch(ctx, gid, nil, nil); err == nil {
		t.Error("Patch (get) on closed store should error")
	}
}

func TestGroupStore_CCOrigin(t *testing.T) {
	_, path := newTestLog(t)
	gs := newTestGroups(t, path)
	ctx := context.Background()

	set, err := gs.CCOriginSet(ctx)
	if err != nil || len(set) != 0 {
		t.Fatalf("empty origin set = %v/%v, want empty/nil", set, err)
	}
	if err := gs.MarkCCOrigin(ctx, "cc:aaa"); err != nil {
		t.Fatal(err)
	}
	// Idempotent: marking twice does not error.
	if err := gs.MarkCCOrigin(ctx, "cc:aaa"); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	if err := gs.MarkCCOrigin(ctx, "cc:bbb"); err != nil {
		t.Fatal(err)
	}
	set, _ = gs.CCOriginSet(ctx)
	if !set["cc:aaa"] || !set["cc:bbb"] || len(set) != 2 {
		t.Errorf("origin set = %v, want {cc:aaa, cc:bbb}", set)
	}
	// Empty id is rejected.
	if err := gs.MarkCCOrigin(ctx, ""); err == nil {
		t.Error("empty id should error")
	}
}

func mustSet(t *testing.T, gs *GroupStore, threadID string, groupID *string) {
	t.Helper()
	if err := gs.SetThreadGroup(context.Background(), threadID, groupID); err != nil {
		t.Fatalf("set thread group %s: %v", threadID, err)
	}
}
