// Package common - common utility structs and functions
package common //revive:disable-line:var-naming

import "fmt"

// GetTypedPtr helper function to convert from arbitrary type to a pointer of that type
func GetTypedPtr[T any](org T) *T {
	return &org
}

// ToString converts any type implementing Stringer to a string.
func ToString[T fmt.Stringer](org T) string {
	return org.String()
}

// SliceToString converts any slice of type implementing Stringer to a string slice.
func SliceToString[T fmt.Stringer](org []T) []string {
	result := make([]string, 0, len(org))
	for _, entry := range org {
		result = append(result, entry.String())
	}
	return result
}
