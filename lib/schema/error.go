package schema

import (
	"errors"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// PrintValidationError writes an indented rendering of the error tree.
func PrintValidationError(w io.Writer, err *jsonschema.ValidationError, indent string) {
	path := err.InstanceLocation
	if path == "" {
		path = "<root>"
	}
	fmt.Fprintf(w, "%sINVALID at %s: %s\n", indent, path, err.Message)
	for _, c := range err.Causes {
		PrintValidationError(w, c, indent+"  ")
	}
}

// PrintValidationOrPlain prints an indented validation tree if err is a
// jsonschema.ValidationError; otherwise it prints err.Error().
func PrintValidationOrPlain(err error, stderr io.Writer) {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		PrintValidationError(stderr, ve, "")
	} else {
		fmt.Fprintln(stderr, "INVALID:", err)
	}
}
