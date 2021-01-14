package token

import (
	"strings"
	"testing"

	"github.com/kasworld/nonkey/tokentype"
)

// Test looking up values succeeds, then fails
func TestLookup(t *testing.T) {

	for key, val := range keywords {

		// Obviously this will pass.
		if LookupIdentifier(string(key)) != val {
			t.Errorf("Lookup of %s failed", key)
		}

		// Once the keywords are uppercase they'll no longer
		// match - so we find them as identifiers.
		if LookupIdentifier(strings.ToUpper(string(key))) != tokentype.IDENT {
			t.Errorf("Lookup of %s failed", key)
		}
	}
}
