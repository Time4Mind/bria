package domain_test

import (
	"testing"
	"time"
)

func TestOriginArchiveInventoryFinalizesCommittedArchiveIdempotently(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "inventory", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.CloseSession(
		1, ref, session.Revision, "archive-inventory", time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	before := state.Sessions[ref.Key()]
	if err := state.ObserveArchiveInventory(
		"alpha", []string{"archive-inventory"}, time.Unix(21, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	ready := state.Sessions[ref.Key()]
	if !ready.ArchiveReady || ready.Revision != before.Revision+1 {
		t.Fatalf("ready archive=%#v", ready)
	}
	if err := state.ObserveArchiveInventory(
		"alpha", []string{"archive-inventory"}, time.Unix(22, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if got := state.Sessions[ref.Key()].Revision; got != ready.Revision {
		t.Fatalf("idempotent inventory revision=%d, want %d", got, ready.Revision)
	}
}

func TestAnotherNodeCannotFinalizeArchive(t *testing.T) {
	state := fixtureState(t)
	ref := addSession(t, state, "origin", "alpha", 1, time.Unix(10, 0).UTC())
	session := state.Sessions[ref.Key()]
	if err := state.CloseSession(
		1, ref, session.Revision, "archive-origin", time.Unix(20, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if err := state.ObserveArchiveInventory(
		"beta", []string{"archive-origin"}, time.Unix(21, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	if state.Sessions[ref.Key()].ArchiveReady {
		t.Fatal("another node finalized an origin-local archive")
	}
}
