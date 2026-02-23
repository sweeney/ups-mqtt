package publisher

// FakePublisher records every published Message so tests can inspect them.
type FakePublisher struct {
	Messages     []Message
	PublishError error
	Closed       bool
}

// Publish appends the message to the recorded list, or returns PublishError
// if set.
func (f *FakePublisher) Publish(msg Message) error {
	if f.PublishError != nil {
		return f.PublishError
	}
	f.Messages = append(f.Messages, msg)
	return nil
}

// Close marks the publisher as closed.
func (f *FakePublisher) Close() error {
	f.Closed = true
	return nil
}

// Find returns the first Message whose Topic matches, plus a found bool.
func (f *FakePublisher) Find(topic string) (Message, bool) {
	for _, m := range f.Messages {
		if m.Topic == topic {
			return m, true
		}
	}
	return Message{}, false
}

// Reset clears all recorded state so the fake can be reused between sub-tests.
func (f *FakePublisher) Reset() {
	f.Messages = nil
	f.PublishError = nil
	f.Closed = false
}
