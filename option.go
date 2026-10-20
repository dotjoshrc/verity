package verity

import "testing"

// Option is a function that can be used to configure a TestSuite. It takes a
// testing.T and a TestSuite as arguments and returns an error if the
// configuration fails.
type Option func(*testing.T, TestSuite) error
