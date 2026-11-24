//go:build !race

package dockercat_test

// timeScale stretches time bounds under the race detector; 1 without it.
const timeScale = 1
