package app

import (
	"context"
	"crypto/sha256"
	"fmt"

	"duplex/internal/library"
)

// The root changes when a channel gains or loses its last visible recording,
// even if the configuration does not change.
func (p catalogueFS) rootInfo(ctx context.Context) (library.Node, error) {
	children, err := p.rootChildren(ctx, "")
	if err == nil {
		err = ctx.Err()
	}

	if err != nil {
		return library.Node{}, err
	}

	digest := sha256.New()
	for _, child := range children {
		digest.Write([]byte(child.Name + "\x00"))
	}

	return library.Node{
		Directory: true,
		Modified:  p.app.Config.Snapshot().LoadedAt.Unix(),
		Revision:  fmt.Sprintf("%x", digest.Sum(nil)),
	}, nil
}
