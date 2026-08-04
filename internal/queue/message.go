package queue

// JobSubmission is the wire payload published to SubmitSubj by the API server
// and consumed by the operator to create the AcceleratorJob CRD.
type JobSubmission struct {
	ID               string   `json:"id"`
	Team             string   `json:"team"`
	Image            string   `json:"image"`
	Command          []string `json:"command,omitempty"`
	Args             []string `json:"args,omitempty"`
	AcceleratorType  string   `json:"acceleratorType"`
	AcceleratorCount int32    `json:"acceleratorCount"`
	Priority         int32    `json:"priority,omitempty"`
}
