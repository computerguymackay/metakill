package fixtures

import _ "embed"

// bundle7z is made by `go run ./internal/fixtures/gen7z` (needs 7-Zip): there
// is no pure-Go 7z writer.
//
//go:embed data/bundle.7z
var bundle7z []byte

// Bundle7Z returns the 7z fixture: modification, creation and access times,
// a photo with EXIF, an executable script, a .DS_Store file and a
// home-directory path.
func Bundle7Z() []byte { return append([]byte(nil), bundle7z...) }

func init() {
	register("bundle.7z", func() Fixture { return Fixture{Name: "bundle.7z", Format: "7z", Data: Bundle7Z()} })
}
