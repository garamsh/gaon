package machines

// Refusal is a plugin's refusal of a Machine. The Machine fails with Reason
// in its Ready condition and Message as the condition's message.
type Refusal struct {
	Reason  string
	Message string
}

func (r *Refusal) Error() string { return r.Message }
