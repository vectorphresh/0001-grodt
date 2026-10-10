//go:build !race

package main

import "time"

const incompleteProcessWait = 60 * time.Second
