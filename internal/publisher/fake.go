package publisher

import "fmt"

// FakePublisher records every published Message so tests can inspect them.
type FakePublisher struct {
	Messages     []Message
	PublishError error
	// FailTopic, when set, selects which topics should fail.  Topics it does
	// not select are published normally.  This makes it possible to simulate a
	// partially degraded link — the case where one topic fails and everything
	// after it must still be attempted.  When nil, PublishError (if set)
	// applies to every topic.
	FailTopic func(topic string) bool
	// Failed records the topics whose publish returned an error, in order.
	Failed []string
	Closed bool
}

// Publish appends the message to the recorded list, or returns an error if
// this topic is configured to fail.
func (f *FakePublisher) Publish(msg Message) error {
	if err := f.errorFor(msg.Topic); err != nil {
		f.Failed = append(f.Failed, msg.Topic)
		return err
	}
	f.Messages = append(f.Messages, msg)
	return nil
}

// errorFor reports the error Publish should return for topic, or nil to let it
// through.
func (f *FakePublisher) errorFor(topic string) error {
	if f.FailTopic == nil {
		return f.PublishError
	}
	if !f.FailTopic(topic) {
		return nil
	}
	if f.PublishError != nil {
		return f.PublishError
	}
	return fmt.Errorf("fake publish failure for %q", topic)
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

// IndexOf returns the position of the first Message with the given topic, or
// -1 if it was never published.  Tests use it to assert ordering — notably
// that the outage message precedes the bulk telemetry.
func (f *FakePublisher) IndexOf(topic string) int {
	for i, m := range f.Messages {
		if m.Topic == topic {
			return i
		}
	}
	return -1
}

// Reset clears all recorded state so the fake can be reused between sub-tests.
func (f *FakePublisher) Reset() {
	f.Messages = nil
	f.PublishError = nil
	f.FailTopic = nil
	f.Failed = nil
	f.Closed = false
}
