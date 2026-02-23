package nut

import (
	"errors"
	"testing"
)

func TestFakePoller_Poll_ReturnsVariables(t *testing.T) {
	fp := &FakePoller{
		Variables: []Variable{
			{Name: "ups.status", Value: "OL"},
			{Name: "ups.load", Value: "8"},
		},
	}

	vars, err := fp.Poll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vars) != 2 {
		t.Fatalf("got %d variables, want 2", len(vars))
	}
	if vars[0].Name != "ups.status" || vars[0].Value != "OL" {
		t.Errorf("vars[0] = %+v, want {ups.status OL}", vars[0])
	}
}

func TestFakePoller_Poll_ReturnsError(t *testing.T) {
	fp := &FakePoller{
		Err: errors.New("connection refused"),
	}

	_, err := fp.Poll()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "connection refused" {
		t.Errorf("error = %q, want %q", err.Error(), "connection refused")
	}
}

func TestFakePoller_Poll_RecoverAfterError(t *testing.T) {
	fp := &FakePoller{
		Variables: []Variable{{Name: "ups.status", Value: "OL"}},
		Err:       errors.New("temporary failure"),
	}

	// First poll fails.
	if _, err := fp.Poll(); err == nil {
		t.Fatal("expected error on first poll")
	}

	// Clearing the error simulates reconnect; next poll succeeds.
	fp.Err = nil
	vars, err := fp.Poll()
	if err != nil {
		t.Fatalf("expected success after error cleared, got: %v", err)
	}
	if len(vars) != 1 {
		t.Errorf("got %d vars, want 1", len(vars))
	}
}

func TestFakePoller_CallCount(t *testing.T) {
	fp := &FakePoller{}
	for i := 1; i <= 3; i++ {
		fp.Poll() //nolint:errcheck
		if fp.CallCount != i {
			t.Errorf("CallCount = %d after %d calls, want %d", fp.CallCount, i, i)
		}
	}
}

func TestFakePoller_Close(t *testing.T) {
	fp := &FakePoller{}
	if fp.Closed {
		t.Fatal("Closed should be false initially")
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if !fp.Closed {
		t.Error("Closed should be true after Close()")
	}
}

func TestFakePoller_Reset(t *testing.T) {
	fp := &FakePoller{
		Variables: []Variable{{Name: "ups.load", Value: "50"}},
		Err:       errors.New("some error"),
		CallCount: 5,
		Closed:    true,
	}
	fp.Reset()

	if fp.Variables != nil {
		t.Error("Reset should clear Variables")
	}
	if fp.Err != nil {
		t.Error("Reset should clear Err")
	}
	if fp.CallCount != 0 {
		t.Errorf("Reset should set CallCount=0, got %d", fp.CallCount)
	}
	if fp.Closed {
		t.Error("Reset should set Closed=false")
	}
}

func TestFakePoller_Sequence_StepsThrough(t *testing.T) {
	seq := [][]Variable{
		{{Name: "ups.status", Value: "OL"}},
		{{Name: "ups.status", Value: "OB DISCHRG"}},
		{{Name: "ups.status", Value: "OL CHRG"}},
	}
	fp := &FakePoller{Sequence: seq}

	for i, want := range []string{"OL", "OB DISCHRG", "OL CHRG"} {
		vars, err := fp.Poll()
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
		if vars[0].Value != want {
			t.Errorf("call %d: ups.status = %q, want %q", i+1, vars[0].Value, want)
		}
	}
}

func TestFakePoller_Sequence_RepeatsLastElement(t *testing.T) {
	fp := &FakePoller{
		Sequence: [][]Variable{
			{{Name: "ups.status", Value: "OB DISCHRG"}},
		},
	}
	for i := 0; i < 3; i++ {
		vars, err := fp.Poll()
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
		if vars[0].Value != "OB DISCHRG" {
			t.Errorf("call %d: ups.status = %q, want OB DISCHRG", i+1, vars[0].Value)
		}
	}
}

func TestFakePoller_Reset_ClearsSequence(t *testing.T) {
	fp := &FakePoller{
		Sequence: [][]Variable{{{Name: "ups.status", Value: "OL"}}},
	}
	fp.Reset()
	if fp.Sequence != nil {
		t.Error("Reset should clear Sequence")
	}
}

func TestFakePoller_Poll_ReturnsCopy(t *testing.T) {
	fp := &FakePoller{
		Variables: []Variable{{Name: "a", Value: "1"}},
	}
	vars, _ := fp.Poll()
	vars[0].Value = "mutated"

	// Original should be unchanged.
	if fp.Variables[0].Value != "1" {
		t.Error("Poll should return a copy, not a reference to the underlying slice")
	}
}
