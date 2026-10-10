package state

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestJournalSinceFiltersCopiesAndAdvances(t *testing.T) {
	s := planStore(t)
	s.RecordPlanEvaluation(json.RawMessage(`{"large":"excluded"}`), json.RawMessage(`null`))
	s.Diagnostic("exclusive_host_correction_started")
	entries, cursor := s.JournalSince(0, "runtime_failure")
	if len(entries) != 1 || cursor != s.Snapshot().Intrinsic.JournalSequence {
		t.Fatal(entries, cursor)
	}
	entries[0].Data[0] = '!'
	if !json.Valid(s.Journal()[len(s.Journal())-1].Data) {
		t.Fatal("journal alias")
	}
	if err := s.BeginCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, next := s.JournalSince(cursor, "runtime_failure")
	if len(entries) != 0 || next <= cursor {
		t.Fatal("unselected entries did not advance cursor")
	}
	s.RecordToolRecovery(bytes.Repeat([]byte("x"), 2049))
	if _, last := s.JournalSince(next); last != next {
		t.Fatal("oversized diagnostic retained")
	}
}

func TestJournalSincePlanExcludesArchivedTaskInput(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.Push(ctx, "Goal", strings.Repeat("private archive", 100000)); err != nil {
		t.Fatal(err)
	}
	if err := s.RevisePlan(ctx, Plan{Description: "Inspection", Status: "active", Steps: []Step{{ID: "inspect", Description: "Inspect", Status: "pending"}}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.JournalSince(0, "task_plan_revised")
	if len(entries) != 1 || len(entries[0].Data) > 512 || bytes.Contains(entries[0].Data, []byte("private archive")) {
		t.Fatal("archived payload projected")
	}
	if !bytes.Contains(encode(s.Journal()), []byte("private archive")) {
		t.Fatal("archive lost")
	}
}
