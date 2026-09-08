package artifactstore

import (
	"context"
	"crypto/sha256"
	"io"
)

func artifactDigest(ctx context.Context, reader io.Reader) (string, int64, error) {
	digest := sha256.New()
	size, err := io.Copy(digest, contextReader{ctx: ctx, reader: reader})
	return digestHex(digest), size, err
}
