package companiontest

import "testing"

func TestMemBlobConformance(t *testing.T) {
	BlobConformance(t, NewMemBlob().Bucket("home-page-companion/test"), 0)
}
