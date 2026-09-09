//go:build linux

package readbox

import "unsafe"

// The two syscall arguments Landlock needs that Go's type system will not
// produce on its own. They are isolated here so the file that expresses the
// policy contains no unsafe code.

func unsafePointer[T any](v *T) uintptr { return uintptr(unsafe.Pointer(v)) }

func unsafeSizeof[T any](v T) uintptr { return unsafe.Sizeof(v) }
