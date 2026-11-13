package readonly

import "errors"

type initializationError struct{ category string }

func (e initializationError) Error() string       { return "READONLY_INIT_" + e.category }
func (e initializationError) Unwrap() error       { return ErrUnverified }
func initializationFailure(category string) error { return initializationError{category: category} }

// InitializationCategory returns only an owned enum, never an underlying driver
// message, connection value, path, SQL or returned cell.
func InitializationCategory(err error) string {
	var own initializationError
	if errors.As(err, &own) {
		return own.category
	}
	return "unverified"
}
