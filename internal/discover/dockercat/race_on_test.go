//go:build race

package dockercat_test

// timeScale stretches time bounds: the race detector slows the YAML
// parser several times over.
const timeScale = 5
