package gpu

// SetStoreLimit sets the bound of the texture store for a test and
// returns a function restoring it.
func SetStoreLimit(n int) func() {
	old := storeLimit
	storeLimit = n
	return func() { storeLimit = old }
}
