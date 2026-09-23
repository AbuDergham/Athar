//go:build !windows

package update

// assetName: self-update ships only Windows exes (other platforms get
// archives), so Install refuses elsewhere.
func assetName(version string) (string, error) { return "", ErrUnsupported }

// replaceExe is not used outside Windows.
func replaceExe(exe, next string) error { return ErrUnsupported }
