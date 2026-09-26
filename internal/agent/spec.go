package agent

// RunSpec defines the task and procedure. Runtime owns a value copy for the run;
// it is supplied separately from mutable progress and cannot be patched.
type RunSpec struct {
	Goal         string `json:"goal"`
	Instructions string `json:"instructions,omitempty"`
}
