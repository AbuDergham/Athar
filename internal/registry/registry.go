// Package registry lists every adapter. Adapters live in their own packages
// under internal/adapters/<id>/ so they can be developed independently.
// Adding an agent = one package + one line here + testdata fixtures.
package registry

import "github.com/AbuDergham/Athar/internal/adapters"

// All returns every adapter, most specific path roots first.
func All() []adapters.Adapter {
	return []adapters.Adapter{
		// Filled in as adapters are implemented (see docs/superpowers/plans).
	}
}
