package main

import (
	"errors"
)

// checkTokens verifies there are no unrecognised tokens. With the rewritten
// tokenizer, lexing errors are returned directly from tokenizer(), so this is
// now a lightweight guard for any residual nullToken.
func checkTokens(tokens []token) error {
	for _, tokenVal := range tokens {
		if tokenVal.group == nullToken {
			return errors.New("This token was not recognised: " + tokenVal.value)
		}
	}
	return nil
}
