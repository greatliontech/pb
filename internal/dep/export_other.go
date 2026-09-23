//go:build !unix

package dep

import "io/fs"

// sameDirectory has no identity to compare where the working tree's
// infos carry no Unix stat; two directories are the same by spelling
// alone there (REQ-export-output's identity rule is Unix-shaped, as
// the working tree is).
func sameDirectory(a, b fs.FileInfo) bool { return false }
