//go:build race

package main

import "time"

// The child process is the same instrumented test executable, so its complete
// 500-cycle run also incurs race-detector and schema-validation overhead.
const incompleteProcessWait = 5 * time.Minute
