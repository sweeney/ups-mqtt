package nut

// FakePoller is a test double for Poller.
//
// Single-snapshot mode: pre-seed Variables; every Poll() returns that slice.
// Sequence mode: pre-seed Sequence; each Poll() returns the next element.
// When the sequence is exhausted the last element is repeated, simulating a
// steady post-event state.  Set Err to inject a failure on every call.
type FakePoller struct {
	Variables []Variable   // returned when Sequence is nil/empty
	Sequence  [][]Variable // each Poll() advances through this list
	Err       error
	CallCount int
	Closed    bool
}

// Poll returns the pre-seeded variables for the current call index,
// or Err if set.
func (f *FakePoller) Poll() ([]Variable, error) {
	f.CallCount++
	if f.Err != nil {
		return nil, f.Err
	}

	src := f.Variables
	if len(f.Sequence) > 0 {
		idx := f.CallCount - 1
		if idx >= len(f.Sequence) {
			idx = len(f.Sequence) - 1 // repeat last element
		}
		src = f.Sequence[idx]
	}

	out := make([]Variable, len(src))
	copy(out, src)
	return out, nil
}

// Close records that the poller was closed.
func (f *FakePoller) Close() error {
	f.Closed = true
	return nil
}

// Reset clears all state so the fake can be reused between sub-tests.
func (f *FakePoller) Reset() {
	f.Variables = nil
	f.Sequence = nil
	f.Err = nil
	f.CallCount = 0
	f.Closed = false
}
