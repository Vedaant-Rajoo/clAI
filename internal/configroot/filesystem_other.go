//go:build !darwin && !linux

package configroot

// Canonicalize preserves the selected platform root on non-Unix targets. It
// validates and cleans the path but intentionally makes no Unix filesystem or
// permission guarantees.
func Canonicalize(path string, _ bool) (string, error) {
	return validateBase("config root", path)
}
