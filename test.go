package verity

import "testing"

// TestHookFn is a function type that can be used for BeforeFunc and AfterFunc
// hooks in tests.
type TestHookFunc func(t *testing.T)

// Test represents a single test case.
type Test[TReq, TRes any] struct {
	Name         string
	Request      TReq
	WantResponse TRes
	WantErr      error
	BeforeFunc   TestHookFunc
	AfterFunc    TestHookFunc
}

// Tests is a slice of Test cases.
type Tests[TReq, TRes any] []Test[TReq, TRes]

// Run executes all the tests in the Tests slice. It takes a testing.T instance
// and a test function that defines how to execute each test case. The test
// function receives the testing.T instance and the Test case as arguments. The
// Run method handles running each test case, including executing any BeforeFunc
// and AfterFunc hooks if they are defined.
func (tests Tests[TReq, TRes]) Run(t *testing.T, testFn func(t *testing.T, test Test[TReq, TRes])) {
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			if test.BeforeFunc != nil {
				test.BeforeFunc(t)
			}

			// Through t.Cleanup so it also runs when testFn fails fatally.
			if test.AfterFunc != nil {
				t.Cleanup(func() { test.AfterFunc(t) })
			}

			testFn(t, test)
		})
	}
}
