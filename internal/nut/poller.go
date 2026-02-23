package nut

// Variable holds a single NUT variable name/value pair.
// Value is always normalised to a string; callers parse as needed.
type Variable struct {
	Name  string
	Value string
}

// Poller abstracts the NUT data source so tests can inject a fake.
type Poller interface {
	Poll() ([]Variable, error)
	Close() error
}

// VarsToMap converts a []Variable slice into a name→value map for downstream
// use (metrics computation, topic publishing, etc.).
func VarsToMap(vars []Variable) map[string]string {
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		m[v.Name] = v.Value
	}
	return m
}
