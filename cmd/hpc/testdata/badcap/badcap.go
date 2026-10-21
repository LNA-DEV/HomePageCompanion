// Package badcap is a capability hpc gen must refuse: a struct with a
// []byte field would carry binary data inside JSON.
package badcap

import (
	"context"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

type Uploader interface {
	Upload(ctx context.Context, f File) error
}

type File struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

var UploaderV1 = companion.NewCapability[Uploader]("x.test.uploader", 1)
