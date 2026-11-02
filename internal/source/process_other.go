//go:build !unix

package source

import "os/exec"

func configureResolverProcess(_ *exec.Cmd) {}
